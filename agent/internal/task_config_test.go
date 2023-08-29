package internal

import (
	"path/filepath"
	"testing"

	"github.com/evergreen-ci/evergreen/apimodels"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/model/patch"
	"github.com/evergreen-ci/evergreen/model/task"
	"github.com/evergreen-ci/evergreen/testutil"
	"github.com/evergreen-ci/evergreen/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskConfigGetWorkingDirectory(t *testing.T) {
	curdir := testutil.GetDirectoryOfFile()

	conf := &TaskConfig{
		WorkDir: curdir,
	}

	// make sure that we fall back to the configured working directory
	out, err := conf.GetWorkingDirectory("")
	assert.NoError(t, err)
	assert.Equal(t, conf.WorkDir, out)

	// check for a directory that we know exists
	out, err = conf.GetWorkingDirectory("testutil")
	require.NoError(t, err)
	assert.Equal(t, out, filepath.Join(curdir, "testutil"))

	// check for a file not a directory
	out, err = conf.GetWorkingDirectory("task_config.go")
	assert.Error(t, err)
	assert.Equal(t, "", out)

	// presumably for a directory that doesn't exist
	out, err = conf.GetWorkingDirectory("does-not-exist")
	assert.Error(t, err)
	assert.Equal(t, "", out)
}

func TestNewTaskConfig(t *testing.T) {
	curdir := testutil.GetDirectoryOfFile()
	taskName := "some_task"
	bvName := "bv"
	p := &model.Project{
		Tasks: []model.ProjectTask{
			{
				Name: taskName,
			},
		},
		BuildVariants: []model.BuildVariant{{Name: bvName}},
	}
	task := &task.Task{
		Id:           "task_id",
		DisplayName:  taskName,
		BuildVariant: bvName,
		Version:      "v1",
	}

	taskConfig, err := NewTaskConfig(curdir, &apimodels.DistroView{}, p, task, &model.ProjectRef{
		Id:         "project_id",
		Identifier: "project_identifier",
	}, &patch.Patch{}, util.Expansions{})
	assert.NoError(t, err)

	assert.Equal(t, util.Expansions{}, taskConfig.DynamicExpansions)
	assert.Equal(t, &util.Expansions{}, taskConfig.Expansions)
	assert.Equal(t, &apimodels.DistroView{}, taskConfig.Distro)
	assert.Equal(t, p, taskConfig.Project)
	assert.Equal(t, task, taskConfig.Task)
}
