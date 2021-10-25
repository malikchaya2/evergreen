package command

import (
	"context"
	"io/ioutil"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/agent/internal/client"
	agentutil "github.com/evergreen-ci/evergreen/agent/internal/testutil"
	"github.com/evergreen-ci/evergreen/apimodels"
	"github.com/evergreen-ci/evergreen/model"
	modelutil "github.com/evergreen-ci/evergreen/model/testutil"
	"github.com/evergreen-ci/evergreen/testutil"
	"github.com/evergreen-ci/evergreen/util"
	"github.com/evergreen-ci/utility"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/stretchr/testify/require"
)

func TestS3CopyPluginExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := evergreen.GetEnvironment()
	testConfig := env.Settings()
	testutil.ConfigureIntegrationTest(t, testConfig, "TestS3CopyPluginExecution")

	comm := client.NewMock("http://localhost.com")
	Convey("the s3 copy command should execute successfully", func() {
		//r := "{   evergreen-staging.corp.mongodb.com map[] map[] %!s(*multipart.Form=<nil>) map[] 52.3.244.89 /api/2/task/evg_enterprise_rhel_80_64_bit_s3copy_test_push_patch_cda0e58828647e9724f8c5ca219f98d067b715db_618281309dbe32114808c29d_21_11_03_12_31_56/s3Copy/s3Copy %!s(*tls.ConnectionState=<nil>) %!s(<-chan struct {}=<nil>) %!s(*http.Response=<nil>) %!s(*context.valueCtx=&{0xc001835e00 0 0xc001c90b00})}"
		s3CopyReq := &apimodels.S3CopyRequest{}
		//here
		req := &http.Request{Method: "POST",
			URL: &url.URL{
				Path: "/api/2/task/evg_enterprise_rhel_80_64_bit_s3copy_test_push_patch_cda0e58828647e9724f8c5ca219f98d067b715db_618281309dbe32114808c29d_21_11_03_12_31_56/s3Copy/s3Copy",
			},
			Header: http.Header{
				"Accept-Encoding":   {"gzip"},
				"Content-Length":    {"0"},
				"Content-Type":      {"application/json"},
				"Host-Id":           {"i-0c11a5876ed5b0e9a"},
				"Host-Secret":       {"9d869809c680a5cc1499f74f6ef17b7a"},
				"Task-Id":           {"evg_enterprise_rhel_80_64_bit_s3copy_test_push_patch_cda0e58828647e9724f8c5ca219f98d067b715db_618281309dbe32114808c29d_21_11_03_12_31_56"},
				"Task-Secret":       {"e079a280925afb788f4445ad239563e5"},
				"User-Agent":        {"Go-http-client/1.1"},
				"X-Amzn-Trace-Id":   {"Root=1-618286d7-3d02096262b0ce661e4a98f4"},
				"X-Forwarded-For":   {"52.3.244.89"},
				"X-Forwarded-Port":  {"443"},
				"X-Forwarded-Proto": {"https"},
			},
			Body: ioutil.NopCloser(strings.NewReader("")),
		}
		req = req.WithContext(context.WithValue(context.Background(), 0, "/api/2/task/evg_enterprise_rhel_80_64_bit_s3copy_test_push_patch_cda0e58828647e9724f8c5ca219f98d067b715db_618281309dbe32114808c29d_21_11_03_12_31_56/s3Copy/s3Copy"))

		err := utility.ReadJSON(util.NewRequestReader(req), s3CopyReq)
		print(err)
	})

	Convey("With a SimpleRegistry and test project file", t, func() {
		version := &model.Version{
			Id: "versionId",
		}
		So(version.Insert(), ShouldBeNil)

		pwd := testutil.GetDirectoryOfFile()
		configFile := filepath.Join(pwd, "testdata", "plugin_s3_copy.yml")
		modelData, err := modelutil.SetupAPITestData(testConfig, "copyTask", "linux-64", configFile, modelutil.NoPatch)
		require.NoError(t, err, "failed to setup test data")
		conf, err := agentutil.MakeTaskConfigFromModelData(testConfig, modelData)
		require.NoError(t, err)
		conf.WorkDir = pwd
		logger, err := comm.GetLoggerProducer(ctx, client.TaskData{ID: conf.Task.Id, Secret: conf.Task.Secret}, nil)
		So(err, ShouldBeNil)

		require.True(t, len(testConfig.Providers.AWS.EC2Keys) > 0)
		conf.Expansions.Update(map[string]string{
			"aws_key":    testConfig.Providers.AWS.EC2Keys[0].Key,
			"aws_secret": testConfig.Providers.AWS.EC2Keys[0].Secret,
		})

		Convey("the s3 copy command should execute successfully", func() {
			for _, task := range conf.Project.Tasks {
				for _, command := range task.Commands {
					pluginCmds, err := Render(command, conf.Project.Functions)
					require.NoError(t, err, "Couldn't get plugin command: %s", command.Command)
					So(pluginCmds, ShouldNotBeNil)
					So(err, ShouldBeNil)
					err = pluginCmds[0].Execute(ctx, comm, logger, conf)
					So(err, ShouldBeNil)
				}
			}
		})

	})
}
