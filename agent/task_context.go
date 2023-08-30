package agent

import (
	"context"
	"sync"
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/agent/command"
	"github.com/evergreen-ci/evergreen/agent/internal"
	"github.com/evergreen-ci/evergreen/agent/internal/client"
	"github.com/evergreen-ci/evergreen/apimodels"
	"github.com/evergreen-ci/evergreen/model/patch"
	"github.com/evergreen-ci/utility"
	"github.com/mongodb/grip"
	"github.com/mongodb/jasper"
	"github.com/pkg/errors"
)

// do these hae any baring on the command running
// taskConfig is the source of truth
// to maintian state for the task run
// superset
// todo: move to the task context file
type taskContext struct {
	currentCommand            command.Command
	logger                    client.LoggerProducer
	task                      client.TaskData
	ranSetupGroup             bool
	taskConfig                *internal.TaskConfig
	timeout                   timeoutInfo
	oomTracker                jasper.OOMTracker
	traceID                   string
	unsetFunctionVarsDisabled bool
	sync.RWMutex
}

func (tc *taskContext) setCurrentCommand(command command.Command) {
	tc.Lock()
	defer tc.Unlock()
	tc.currentCommand = command
	if tc.logger != nil {
		tc.logger.Execution().Infof("Current command set to %s (%s).", tc.currentCommand.DisplayName(), tc.currentCommand.Type())
	}
}

func (tc *taskContext) getCurrentCommand() command.Command {
	tc.RLock()
	defer tc.RUnlock()
	return tc.currentCommand
}

// setCurrentIdleTimeout sets the idle timeout for the current running command.
// This timeout only applies to commands running in specific blocks where idle
// timeout is allowed.
func (tc *taskContext) setCurrentIdleTimeout(cmd command.Command, block command.BlockType) {
	tc.Lock()
	defer tc.Unlock()

	var timeout time.Duration
	if dynamicTimeout := tc.taskConfig.GetIdleTimeout(); dynamicTimeout != 0 {
		timeout = time.Duration(dynamicTimeout) * time.Second
	} else if cmd.IdleTimeout() > 0 {
		timeout = cmd.IdleTimeout()
	} else {
		timeout = defaultIdleTimeout
	}

	tc.setIdleTimeout(timeout)

	tc.logger.Execution().Debugf("Set idle timeout for %s (%s) to %s.",
		cmd.DisplayName(), cmd.Type(), tc.getIdleTimeout())
}

// getCurrentIdleTimeout returns the idle timeout for the current running
// command.
func (tc *taskContext) getCurrentIdleTimeout() time.Duration {
	tc.RLock()
	defer tc.RUnlock()

	timeout := tc.getIdleTimeout()
	if timeout > 0 {
		return timeout
	}
	return defaultIdleTimeout
}

func (tc *taskContext) reachTimeOut(kind timeoutType, dur time.Duration) {
	tc.Lock()
	defer tc.Unlock()

	tc.setTimedOut(true, kind)
	tc.setTimeoutDuration(dur)
}

func (tc *taskContext) hadTimedOut() bool {
	tc.RLock()
	defer tc.RUnlock()

	return tc.timedOut()
}

func (tc *taskContext) getOomTrackerInfo() *apimodels.OOMTrackerInfo {
	lines, pids := tc.oomTracker.Report()
	if len(lines) == 0 {
		return nil
	}

	return &apimodels.OOMTrackerInfo{
		Detected: true,
		Pids:     pids,
	}
}

func (tc *taskContext) oomTrackerEnabled(cloudProvider string) bool {
	return tc.taskConfig.Project.OomTracker && !utility.StringSliceContains(evergreen.ProviderContainer, cloudProvider)
}

func (tc *taskContext) setIdleTimeout(dur time.Duration) {
	tc.timeout.idleTimeoutDuration = dur
}

func (tc *taskContext) getIdleTimeout() time.Duration {
	return tc.timeout.idleTimeoutDuration
}

func (tc *taskContext) setTimedOut(timeout bool, kind timeoutType) {
	tc.timeout.hadTimeout = timeout
	tc.timeout.timeoutType = kind
}

func (tc *taskContext) timedOut() bool {
	return tc.timeout.hadTimeout
}

func (tc *taskContext) setTimeoutDuration(dur time.Duration) {
	tc.timeout.exceededDuration = dur
}

func (tc *taskContext) getTimeoutDuration() time.Duration {
	return tc.timeout.exceededDuration
}

func (tc *taskContext) getTimeoutType() timeoutType {
	return tc.timeout.timeoutType
}

// makeTaskConfig fetches task configuration data required to run the task from the API server.
func (a *Agent) makeTaskConfig(ctx context.Context, tc *taskContext) (*internal.TaskConfig, error) {
	if tc.taskConfig != nil && tc.taskConfig.Project.Identifier != "" {
		return tc.taskConfig, nil
	}

	grip.Info("Fetching project config.")
	task, project, expansions, redacted, err := a.fetchProjectConfig(ctx, tc)
	if err != nil {
		return nil, err
	}

	grip.Info("Fetching distro configuration.")
	var confDistro *apimodels.DistroView
	if a.opts.Mode == HostMode {
		confDistro, err = a.comm.GetDistroView(ctx, tc.task)
		if err != nil {
			return nil, err
		}
	}

	grip.Info("Fetching project ref.")
	confRef, err := a.comm.GetProjectRef(ctx, tc.task)
	if err != nil {
		return nil, err
	}
	if confRef == nil {
		return nil, errors.New("agent retrieved an empty project ref")
	}

	var confPatch *patch.Patch
	requester := ""
	tg := ""
	if tc.taskConfig != nil && tc.taskConfig.Task != nil {
		requester = tc.taskConfig.Task.Requester
		tg = tc.taskConfig.Task.TaskGroup
	}
	if evergreen.IsGitHubPatchRequester(requester) {
		grip.Info("Fetching patch document for GitHub PR request.")
		confPatch, err = a.comm.GetTaskPatch(ctx, tc.task, "")
		if err != nil {
			return nil, errors.Wrap(err, "fetching patch for GitHub PR request")
		}
	}

	grip.Info("Constructing task config.")
	taskConfig, err := internal.NewTaskConfig(a.opts.WorkingDirectory, confDistro, project, task, confRef, confPatch, expansions)
	if err != nil {
		return nil, err
	}
	taskConfig.Redacted = redacted
	taskConfig.TaskSync = a.opts.SetupData.TaskSync
	taskConfig.EC2Keys = a.opts.SetupData.EC2Keys

	if tg != "" {
		taskConfig.TaskGroup = *project.FindTaskGroup(tc.taskConfig.Task.TaskGroup)
		if taskConfig.TaskGroup.Name == "" {
			return nil, errors.Wrap(err, "programmatic error: task group is nil")
		}

	}

	return taskConfig, nil
}

func (tc *taskContext) getExecTimeout() time.Duration {
	tc.RLock()
	defer tc.RUnlock()
	if tc.taskConfig == nil {
		return DefaultExecTimeout
	}
	if dynamicTimeout := tc.taskConfig.GetExecTimeout(); dynamicTimeout > 0 {
		return time.Duration(dynamicTimeout) * time.Second
	}
	if pt := tc.taskConfig.Project.FindProjectTask(tc.taskConfig.Task.DisplayName); pt != nil && pt.ExecTimeoutSecs > 0 {
		return time.Duration(pt.ExecTimeoutSecs) * time.Second
	}
	if tc.taskConfig.Project.ExecTimeoutSecs > 0 {
		return time.Duration(tc.taskConfig.Project.ExecTimeoutSecs) * time.Second
	}
	return DefaultExecTimeout
}

func (tc *taskContext) setTaskConfig(taskConfig *internal.TaskConfig) {
	tc.Lock()
	defer tc.Unlock()
	tc.taskConfig = taskConfig
}

func (tc *taskContext) getTaskConfig() *internal.TaskConfig {
	tc.RLock()
	defer tc.RUnlock()
	return tc.taskConfig
}
