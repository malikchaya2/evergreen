package cloud

import (
	"context"
	"testing"

	"github.com/evergreen-ci/cocoa"
	"github.com/evergreen-ci/cocoa/mock"
	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/model/pod"
	"github.com/evergreen-ci/utility"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMakeECSClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Run("Succeeds", func(t *testing.T) {
		c, err := MakeECSClient(validPodClientSettings())
		assert.NoError(t, err)
		assert.NotZero(t, c)
		assert.NoError(t, c.Close(ctx))
	})
	t.Run("FailsWithoutRequiredSettings", func(t *testing.T) {
		c, err := MakeECSClient(&evergreen.Settings{})
		assert.Error(t, err)
		assert.Zero(t, c)
	})
}

func TestMakeSecretsManagerClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Run("Succeeds", func(t *testing.T) {
		c, err := MakeSecretsManagerClient(validPodClientSettings())
		assert.NoError(t, err)
		assert.NotZero(t, c)
		assert.NoError(t, c.Close(ctx))
	})
	t.Run("FailsWithoutRequiredSettings", func(t *testing.T) {
		c, err := MakeSecretsManagerClient(&evergreen.Settings{})
		assert.Error(t, err)
		assert.Zero(t, c)
	})
}

func TestMakeSecretsManagerVault(t *testing.T) {
	t.Run("Succeeds", func(t *testing.T) {
		c, err := MakeSecretsManagerClient(validPodClientSettings())
		require.NoError(t, err)
		assert.NotZero(t, MakeSecretsManagerVault(c))
	})
}

func TestMakeECSPodCreator(t *testing.T) {
	t.Run("Succeeds", func(t *testing.T) {
		c, err := MakeECSPodCreator(&mock.ECSClient{}, &mock.Vault{})
		require.NoError(t, err)
		assert.NotZero(t, c)
	})
	t.Run("FailsWithoutRequiredClient", func(t *testing.T) {
		c, err := MakeECSPodCreator(nil, &mock.Vault{})
		require.Error(t, err)
		assert.Zero(t, c)
	})
}

func TestExportPod(t *testing.T) {
	for tName, tCase := range map[string]func(ctx context.Context, t *testing.T, p *pod.Pod, c cocoa.ECSClient, v cocoa.Vault){
		"Succeeds": func(ctx context.Context, t *testing.T, p *pod.Pod, c cocoa.ECSClient, v cocoa.Vault) {
			exported, err := ExportPod(p, c, v)
			require.NoError(t, err)

			info, err := exported.Info(ctx)
			require.NoError(t, err)

			r := ExportPodResources(p.Resources)
			assert.Equal(t, r, info.Resources)

			s, err := ExportPodStatus(p.Status)
			require.NoError(t, err)
			assert.Equal(t, s, info.Status)
		},
		"FailsWithEmptyPod": func(ctx context.Context, t *testing.T, p *pod.Pod, c cocoa.ECSClient, v cocoa.Vault) {
			exported, err := ExportPod(&pod.Pod{}, c, v)
			assert.Error(t, err)
			assert.Zero(t, exported)
		},
		"FailsWithInvalidECSClientAndVault": func(ctx context.Context, t *testing.T, p *pod.Pod, _ cocoa.ECSClient, _ cocoa.Vault) {
			exported, err := ExportPod(p, nil, nil)
			assert.Error(t, err)
			assert.Zero(t, exported)
		},
	} {
		t.Run(tName, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			p := pod.Pod{
				ID:     "id",
				Status: pod.StatusRunning,
				Resources: pod.ResourceInfo{
					ExternalID:   "task_id",
					DefinitionID: "task_def_id",
					Cluster:      "cluster",
					SecretIDs:    []string{"secret"},
				},
			}
			ecsClient, err := MakeECSClient(validPodClientSettings())
			require.NoError(t, err)
			defer func() {
				assert.NoError(t, ecsClient.Close(ctx))
			}()
			smClient, err := MakeSecretsManagerClient(validPodClientSettings())
			require.NoError(t, err)
			defer func() {
				assert.NoError(t, smClient.Close(ctx))
			}()
			vault := MakeSecretsManagerVault(smClient)

			tCase(ctx, t, &p, ecsClient, vault)
		})
	}
}

func TestExportPodStatus(t *testing.T) {
	t.Run("SucceedsWithStartingStatus", func(t *testing.T) {
		s, err := ExportPodStatus(pod.StatusStarting)
		require.NoError(t, err)
		assert.Equal(t, cocoa.StatusStarting, s)
	})
	t.Run("SucceedsWithRunningStatus", func(t *testing.T) {
		s, err := ExportPodStatus(pod.StatusRunning)
		require.NoError(t, err)
		assert.Equal(t, cocoa.StatusRunning, s)
	})
	t.Run("SucceedsWithTerminatedStatus", func(t *testing.T) {
		s, err := ExportPodStatus(pod.StatusTerminated)
		require.NoError(t, err)
		assert.Equal(t, cocoa.StatusDeleted, s)
	})
	t.Run("FailsWithInitializingStatus", func(t *testing.T) {
		s, err := ExportPodStatus(pod.StatusInitializing)
		assert.Error(t, err)
		assert.Zero(t, s)
	})
	t.Run("FailsWithInvalidStatus", func(t *testing.T) {
		s, err := ExportPodStatus("")
		assert.Error(t, err)
		assert.Zero(t, s)
	})
}

func TestExportPodResources(t *testing.T) {
	t.Run("SetsNoFields", func(t *testing.T) {
		assert.Zero(t, ExportPodResources(pod.ResourceInfo{}))
	})
	t.Run("SetsTaskID", func(t *testing.T) {
		id := "task_id"
		r := ExportPodResources(pod.ResourceInfo{
			ExternalID: id,
		})
		assert.Equal(t, id, utility.FromStringPtr(r.TaskID))
		assert.Zero(t, r.TaskDefinition)
		assert.Zero(t, r.Cluster)
		assert.Zero(t, r.Secrets)
	})
	t.Run("SetsCluster", func(t *testing.T) {
		cluster := "cluster"
		r := ExportPodResources(pod.ResourceInfo{
			Cluster: cluster,
		})
		assert.Equal(t, cluster, utility.FromStringPtr(r.Cluster))
		assert.Zero(t, r.TaskID)
		assert.Zero(t, r.TaskDefinition)
		assert.Zero(t, r.Secrets)
	})
	t.Run("SetsTaskDefinitionID", func(t *testing.T) {
		id := "task_def_id"
		r := ExportPodResources(pod.ResourceInfo{
			DefinitionID: id,
		})
		require.NotZero(t, r.TaskDefinition)
		assert.Equal(t, id, utility.FromStringPtr(r.TaskDefinition.ID))
		assert.True(t, utility.FromBoolPtr(r.TaskDefinition.Owned))
		assert.Zero(t, r.TaskID)
		assert.Zero(t, r.Cluster)
		assert.Zero(t, r.Secrets)
	})
	t.Run("SetsSecrets", func(t *testing.T) {
		secrets := []string{"someSecret", "anotherSecret"}
		r := ExportPodResources(pod.ResourceInfo{
			SecretIDs: secrets,
		})

		require.Len(t, r.Secrets, len(secrets))
		for i, s := range r.Secrets {
			assert.Equal(t, secrets[i], utility.FromStringPtr(s.Name))
			assert.Zero(t, s.Value)
			assert.True(t, utility.FromBoolPtr(s.Owned))
		}
	})
}

func TestExportPodCreationOptions(t *testing.T) {
	t.Run("FailsWithNoECSConfig", func(t *testing.T) {
		opts, err := ExportPodCreationOptions(evergreen.ECSConfig{}, pod.TaskContainerCreationOptions{})
		require.NotZero(t, err)
		require.Zero(t, opts)
	})
	t.Run("FailsWithNoClusterName", func(t *testing.T) {
		opts, err := ExportPodCreationOptions(
			evergreen.ECSConfig{
				TaskRole:      "role",
				ExecutionRole: "role",
				Clusters: []evergreen.ECSClusterConfig{
					{
						Platform: "linux",
					},
				},
			}, pod.TaskContainerCreationOptions{})
		require.NotZero(t, err)
		require.Zero(t, opts)
	})
	t.Run("Succeeds", func(t *testing.T) {
		opts, err := ExportPodCreationOptions(
			evergreen.ECSConfig{
				TaskRole:      "task_role",
				ExecutionRole: "execution_role",
				Clusters: []evergreen.ECSClusterConfig{
					{
						Name: "cluster",
					},
				},
			}, pod.TaskContainerCreationOptions{
				Image:    "image",
				MemoryMB: 128,
				CPU:      128,
				EnvVars: map[string]string{
					"name": "value",
				},
				EnvSecrets: map[string]string{
					"s1": "secret",
				},
			})
		require.Zero(t, err)
		require.NotZero(t, opts)
		require.Equal(t, "task_role", utility.FromStringPtr(opts.TaskRole))
		require.Equal(t, "execution_role", utility.FromStringPtr(opts.ExecutionRole))

		require.NotZero(t, opts.ExecutionOpts)
		require.Equal(t, "cluster", utility.FromStringPtr(opts.ExecutionOpts.Cluster))

		require.NotZero(t, opts.ContainerDefinitions)
		require.Len(t, opts.ContainerDefinitions, 1)
		require.Equal(t, "image", utility.FromStringPtr(opts.ContainerDefinitions[0].Image))
		require.Equal(t, 128, utility.FromIntPtr(opts.ContainerDefinitions[0].MemoryMB))
		require.Equal(t, 128, utility.FromIntPtr(opts.ContainerDefinitions[0].CPU))
		require.Len(t, opts.ContainerDefinitions[0].EnvVars, 2)
		for _, envVar := range opts.ContainerDefinitions[0].EnvVars {
			if envVar.SecretOpts != nil {
				require.Equal(t, "s1", utility.FromStringPtr(envVar.SecretOpts.Name))
				require.Equal(t, "secret", utility.FromStringPtr(envVar.SecretOpts.Value))
				require.Equal(t, false, utility.FromBoolPtr(envVar.SecretOpts.Exists))
				require.Equal(t, true, utility.FromBoolPtr(envVar.SecretOpts.Owned))
			} else {
				require.Equal(t, "name", utility.FromStringPtr(envVar.Name))
				require.Equal(t, "value", utility.FromStringPtr(envVar.Value))
			}
		}
	})
}

func validPodClientSettings() *evergreen.Settings {
	return &evergreen.Settings{
		Providers: evergreen.CloudProviders{
			AWS: evergreen.AWSConfig{
				Pod: evergreen.AWSPodConfig{
					Region: "region",
					Role:   "role",
				},
			},
		},
	}
}
