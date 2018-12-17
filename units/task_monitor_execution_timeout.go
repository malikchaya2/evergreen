package units

import (
	"context"
	"fmt"
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/apimodels"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/model/host"
	"github.com/evergreen-ci/evergreen/model/task"
	"github.com/mongodb/amboy"
	"github.com/mongodb/amboy/dependency"
	"github.com/mongodb/amboy/job"
	"github.com/mongodb/amboy/registry"
	"github.com/mongodb/grip"
	"github.com/mongodb/grip/message"
	"github.com/mongodb/grip/sometimes"
	"github.com/pkg/errors"
)

const (
	heartbeatTimeoutThreshold   = 7 * time.Minute
	taskExecutionTimeoutJobName = "task-execution-timeout"
	maxAttempts                 = 10
)

func init() {
	registry.AddJobType(taskExecutionTimeoutJobName, func() amboy.Job {
		return makeTaskExecutionTimeoutMonitorJob()
	})
}

type taskExecutionTimeoutJob struct {
	Task      string `bson:"task_id"`
	Execution int    `bson:"execution"`
	Attempt   int    `bson:"attempt"`

	successful bool
	job.Base   `bson:"metadata" json:"metadata" yaml:"metadata"`
}

func makeTaskExecutionTimeoutMonitorJob() *taskExecutionTimeoutJob {
	j := &taskExecutionTimeoutJob{
		Base: job.Base{
			JobType: amboy.JobType{
				Name:    taskExecutionTimeoutJobName,
				Version: 0,
			},
		},
	}

	j.SetDependency(dependency.NewAlways())
	return j
}

func NewTaskExecutionMonitorJob(taskID string, execution int, attempt int) amboy.Job {
	j := makeTaskExecutionTimeoutMonitorJob()
	j.Task = taskID
	j.Execution = execution
	j.Attempt = attempt
	j.SetID(fmt.Sprintf("%s.%s.%d.attempt-%d", taskExecutionTimeoutJobName, taskID, execution, attempt))
	return j
}

func (j *taskExecutionTimeoutJob) Run(ctx context.Context) {
	defer j.MarkComplete()

	flags, err := evergreen.GetServiceFlags()
	if err != nil {
		j.AddError(err)
		return
	}
	if flags.MonitorDisabled {
		grip.InfoWhen(sometimes.Percent(evergreen.DegradedLoggingPercent), message.Fields{
			"message":   "monitor is disabled",
			"operation": j.Type().Name,
			"impact":    "skipping task heartbeat cleanup job",
			"mode":      "degraded",
		})
		return
	}
	defer j.tryRequeue()

	t, err := task.FindOneId(j.Task)
	if err != nil {
		j.AddError(errors.Wrap(err, "error finding task"))
		return
	}
	if t == nil {
		j.AddError(errors.New("no task found"))
		return
	}

	msg := message.Fields{
		"operation": j.Type().Name,
		"id":        j.ID(),
		"task":      t.Id,
		"host":      t.HostId,
	}

	err = cleanUpTimedOutTask(t)
	if err != nil {
		grip.Warning(message.WrapError(err, msg))
		j.AddError(err)
	} else {
		j.successful = true
	}
	grip.Debug(msg)
}

func (j *taskExecutionTimeoutJob) tryRequeue() {
	if j.successful || j.Attempt >= maxAttempts {
		return
	}
	newJob := NewTaskExecutionMonitorJob(j.Task, j.Execution, j.Attempt+1)
	newJob.UpdateTimeInfo(amboy.JobTimeInfo{
		WaitUntil: time.Now().Add(time.Minute),
	})
	err := evergreen.GetEnvironment().RemoteQueue().Put(newJob)
	grip.Error(message.WrapError(err, message.Fields{
		"message":  "failed to requeue task timeout job",
		"task":     j.Task,
		"job":      j.ID(),
		"attempts": j.Attempt,
	}))
	j.AddError(err)
}

// function to clean up a single task
func cleanUpTimedOutTask(t *task.Task) error {
	// get tlhe host for the task
	host, err := host.FindOne(host.ById(t.HostId))
	if err != nil {
		return errors.Wrapf(err, "error finding host %s for task %s",
			t.HostId, t.Id)
	}

	// if there's no relevant host, something went wrong
	if host == nil {
		grip.Error(message.Fields{
			"message":   "no entry found for host",
			"task":      t.Id,
			"host":      t.HostId,
			"operation": "cleanup timed out task",
		})
		return errors.WithStack(t.MarkUnscheduled())
	}

	// For a single-host task group, if a task fails, block and dequeue later tasks in that group.
	if t.TaskGroup != "" && t.TaskGroupMaxHosts == 1 && t.Status != evergreen.TaskSucceeded {
		if err = model.BlockTaskGroupTasks(t.Id); err != nil {
			grip.Error(message.WrapError(err, message.Fields{
				"message": "problem blocking task group tasks",
				"task_id": t.Id,
			}))
		}
		grip.Debug(message.Fields{
			"message": "blocked task group tasks for task",
			"task_id": t.Id,
		})
	}

	// if the host still has the task as its running task, clear it.
	if host.RunningTask == t.Id {
		// clear out the host's running task
		if err = host.ClearRunningAndSetLastTask(t); err != nil {
			return errors.Wrapf(err, "error clearing running task %s from host %s", t.Id, host.Id)
		}
	}

	detail := &apimodels.TaskEndDetail{
		Description: task.AgentHeartbeat,
		Type:        evergreen.CommandTypeSystem,
		TimedOut:    true,
		Status:      evergreen.TaskFailed,
	}

	// try to reset the task
	if t.IsPartOfDisplay() {
		err = t.DisplayTask.SetResetWhenFinished()
		if err != nil {
			return errors.Wrap(err, "error requesting task reset")
		}
		return errors.Wrap(model.MarkEnd(t, "monitor", time.Now(), detail, false, &model.StatusChanges{}), "error marking task ended")
	}
	return errors.Wrapf(model.TryResetTask(t.Id, "", "monitor", detail), "error trying to reset task %s", t.Id)
}
