package command

import (
	"context"
	"testing"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandRegistry(t *testing.T) {
	assert := assert.New(t)

	r := newCommandRegistry()
	assert.NotNil(r.cmds)
	assert.NotNil(r.mu)
	assert.NotNil(evgRegistry)

	assert.Len(r.cmds, 0)

	factory := CommandFactory(func() Command { return nil })
	assert.NotNil(factory)
	assert.Error(r.registerCommand("", factory))
	assert.Len(r.cmds, 0)
	assert.Error(r.registerCommand("foo", nil))
	assert.Len(r.cmds, 0)

	assert.NoError(r.registerCommand("cmd.factory", factory))
	assert.Len(r.cmds, 1)
	assert.Error(r.registerCommand("cmd.factory", factory))
	assert.Len(r.cmds, 1)

	retFactory, ok := r.getCommandFactory("cmd.factory")
	assert.True(ok)
	assert.NotNil(retFactory)
}

func TestGlobalCommandRegistryNamesMatchExpectedValues(t *testing.T) {
	assert := assert.New(t)

	evgRegistry.mu.Lock()
	defer evgRegistry.mu.Unlock()
	for name, factory := range evgRegistry.cmds {
		cmd := factory()
		assert.Equal(name, cmd.Name())
	}
}

func TestRenderCommands(t *testing.T) {
	registry := newCommandRegistry()
	registry.cmds = map[string]CommandFactory{
		"command.mock": func() Command { return &mockCommand{} },
	}

	t.Run("NoType", func(t *testing.T) {
		info := model.PluginCommandConf{Command: "command.mock"}
		project := model.Project{}

		cmds, err := registry.renderCommands(info, &project)
		assert.NoError(t, err)
		require.Len(t, cmds, 1)
		assert.Equal(t, model.DefaultCommandType, cmds[0].Type())
	})

	t.Run("ProjectHasType", func(t *testing.T) {
		info := model.PluginCommandConf{Command: "command.mock"}
		project := model.Project{CommandType: evergreen.CommandTypeSetup}

		cmds, err := registry.renderCommands(info, &project)
		assert.NoError(t, err)
		require.Len(t, cmds, 1)
		assert.Equal(t, evergreen.CommandTypeSetup, cmds[0].Type())
	})

	t.Run("ProjectHasPreWithFunc", func(t *testing.T) {
		info := model.PluginCommandConf{Command: "command.mock"}

		multiCommand := `
pre:
  - func: "a_function"
functions:
  a_function:
    command: shell.exec
  purple:
    - command: shell.exec
    - command: shell.exec
  orange:
    - command: shell.exec
`

		p := &model.Project{}
		ctx := context.Background()
		_, err := model.LoadProjectInto(ctx, []byte(multiCommand), nil, "", p)
		assert.NoError(t, err)
		cmds, err := registry.renderCommands(info, p)
		assert.NoError(t, err)
		require.Len(t, cmds, 3)
		// assert.Equal(t, "'command.mock' in pre (#1)", cmds[0].DisplayName())
		// assert.Equal(t, "'command.mock' in pre (#2)", cmds[1].DisplayName())

	})

	t.Run("ProjectHasPre", func(t *testing.T) {
		info := model.PluginCommandConf{Command: "command.mock"}
		multiCommand := `
pre:
  - command: command.mock
    params:
      script: "echo hi"
  - command: command.mock
    params:
      script: "echo hi"
`
		p := &model.Project{}
		ctx := context.Background()
		_, err := model.LoadProjectInto(ctx, []byte(multiCommand), nil, "", p)
		assert.NoError(t, err)
		cmds, err := registry.renderCommands(info, p)
		assert.NoError(t, err)
		require.Len(t, cmds, 3)
		assert.Equal(t, "'command.mock' in pre (#1)", cmds[0].DisplayName())
		assert.Equal(t, "'command.mock' in pre (#2)", cmds[1].DisplayName())

		singleCommand := `
pre:
  command: command.mock
`
		_, err = model.LoadProjectInto(ctx, []byte(singleCommand), nil, "", p)
		assert.NoError(t, err)

		cmds, err = registry.renderCommands(info, p)
		assert.NoError(t, err)
		require.Len(t, cmds, 2)
		assert.Equal(t, "'command.mock' in pre (#1)", cmds[0].DisplayName())
	})

	t.Run("ProjectHasPost", func(t *testing.T) {
		info := model.PluginCommandConf{Command: "command.mock"}
		multiCommand := `
post:
  - command: command.mock
    params:
      script: "echo hi"
  - command: command.mock
    params:
      script: "echo hi"
`
		p := &model.Project{}
		ctx := context.Background()
		_, err := model.LoadProjectInto(ctx, []byte(multiCommand), nil, "", p)
		assert.NoError(t, err)
		cmds, err := registry.renderCommands(info, p)
		assert.NoError(t, err)
		require.Len(t, cmds, 3)
		assert.Equal(t, "'command.mock' in post (#1)", cmds[0].DisplayName())
		assert.Equal(t, "'command.mock' in post (#2)", cmds[1].DisplayName())

		singleCommand := `
post:
  command: command.mock
`
		_, err = model.LoadProjectInto(ctx, []byte(singleCommand), nil, "", p)
		assert.NoError(t, err)

		cmds, err = registry.renderCommands(info, p)
		assert.NoError(t, err)
		require.Len(t, cmds, 2)
		assert.Equal(t, "'command.mock' in post (#1)", cmds[0].DisplayName())
	})

	t.Run("CommandConfHasType", func(t *testing.T) {
		info := model.PluginCommandConf{
			Command: "command.mock",
			Type:    evergreen.CommandTypeSystem,
		}
		project := model.Project{}

		cmds, err := registry.renderCommands(info, &project)
		assert.NoError(t, err)
		require.Len(t, cmds, 1)
		assert.Equal(t, evergreen.CommandTypeSystem, cmds[0].Type())
	})

	t.Run("ConfAndProjectHaveType", func(t *testing.T) {
		info := model.PluginCommandConf{
			Command: "command.mock",
			Type:    evergreen.CommandTypeSystem,
		}
		project := model.Project{CommandType: evergreen.CommandTypeSetup}

		cmds, err := registry.renderCommands(info, &project)
		assert.NoError(t, err)
		require.Len(t, cmds, 1)
		assert.Equal(t, evergreen.CommandTypeSystem, cmds[0].Type())
	})
}
