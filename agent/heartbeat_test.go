package agent

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/10gen-labs/slogger/v1"
	"github.com/evergreen-ci/evergreen/apimodels"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/model/distro"
	"github.com/evergreen-ci/evergreen/model/task"
	"github.com/evergreen-ci/evergreen/model/version"
	. "github.com/smartystreets/goconvey/convey"
)

type MockCommunicator struct {
	shouldFailStart     bool
	shouldFailEnd       bool
	shouldFailHeartbeat bool
	abort               bool
	logChan             chan []model.LogMessage
}

func (*MockCommunicator) tryGet(path string) (*http.Response, error) {
	return nil, nil
}

func (*MockCommunicator) tryPostJSON(path string, data interface{}) (*http.Response, error) {
	return nil, nil
}

func (mc *MockCommunicator) Start(pid string) error {
	if mc.shouldFailStart {
		return fmt.Errorf("failed to start!")
	}
	return nil
}

func (mc *MockCommunicator) End(details *apimodels.TaskEndDetail) (*apimodels.TaskEndResponse, error) {
	if mc.shouldFailEnd {
		return nil, fmt.Errorf("failed to end!")
	}
	return nil, nil
}

func (*MockCommunicator) GetTask() (*task.Task, error) {
	return &task.Task{}, nil
}

func (*MockCommunicator) GetDistro() (*distro.Distro, error) {
	return &distro.Distro{}, nil
}

func (*MockCommunicator) GetProjectRef() (*model.ProjectRef, error) {
	return &model.ProjectRef{}, nil
}

func (*MockCommunicator) GetVersion() (*version.Version, error) {
	return &version.Version{}, nil
}

func (mc *MockCommunicator) Log(logMessages []model.LogMessage) error {
	if mc.shouldFailEnd {
		return fmt.Errorf("failed to end!")
	}
	mc.logChan <- logMessages
	return nil
}

func (mc *MockCommunicator) Heartbeat() (bool, error) {
	if mc.shouldFailHeartbeat {
		return false, fmt.Errorf("failed to heartbeat!")
	}
	return mc.abort, nil
}

func (*MockCommunicator) FetchExpansionVars() (*apimodels.ExpansionVars, error) {
	return &apimodels.ExpansionVars{}, nil
}

func TestHeartbeat(t *testing.T) {

	Convey("With a simple heartbeat ticker", t, func() {
		sigChan := make(chan Signal)
		mockCommunicator := &MockCommunicator{}
		hbTicker := &HeartbeatTicker{
			MaxFailedHeartbeats: 10,
			Interval:            10 * time.Millisecond,
			SignalChan:          sigChan,
			TaskCommunicator:    mockCommunicator,
			Logger: &slogger.Logger{
				Appenders: []slogger.Appender{},
			},
		}

		Convey("abort signals detected by heartbeat are sent on sigChan", func() {
			mockCommunicator.shouldFailHeartbeat = false
			hbTicker.StartHeartbeating()
			go func() {
				time.Sleep(2 * time.Second)
				mockCommunicator.abort = true
			}()
			signal := <-sigChan
			So(signal, ShouldEqual, AbortedByUser)
		})

		Convey("failed heartbeats must signal failure on sigChan", func() {
			mockCommunicator.abort = false
			mockCommunicator.shouldFailHeartbeat = true
			hbTicker.StartHeartbeating()
			signal := <-sigChan
			So(signal, ShouldEqual, HeartbeatMaxFailed)
		})

	})

}
