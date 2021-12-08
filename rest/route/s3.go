package route

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/evergreen-ci/evergreen/apimodels"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/rest/data"
	"github.com/evergreen-ci/evergreen/util"
	"github.com/evergreen-ci/gimlet"
	"github.com/evergreen-ci/pail"
	"github.com/evergreen-ci/utility"
	"github.com/mongodb/grip"
	"github.com/mongodb/grip/message"
	"github.com/pkg/errors"
)

const (
	s3CopyRetryMinDelay = 5 * time.Second
	s3CopyAttempts      = 5
)

////////////////////////////////////////////////////////////////////////
//
// Post /rest/v2/task/{taskId}/s3Copy/s3Copy

type s3CopyHandler struct {
	taskID    string
	s3CopyReq *apimodels.S3CopyRequest
	sc        data.Connector
	count     int
}

func makes3Copy(sc data.Connector) gimlet.RouteHandler {
	return &s3CopyHandler{sc: sc}
}

func (h *s3CopyHandler) Factory() gimlet.RouteHandler { return &s3CopyHandler{sc: h.sc} }

func (h *s3CopyHandler) Parse(ctx context.Context, r *http.Request) error {
	h.count = 0
	taskID := gimlet.GetVars(r)["task_id"]
	if taskID == "" {
		return gimlet.ErrorResponse{
			StatusCode: http.StatusBadRequest,
			Message:    "must provide task ID",
		}
	}
	h.taskID = taskID
	body := util.NewRequestReader(r)
	defer body.Close()
	b, err := ioutil.ReadAll(body)
	if err != nil {
		return errors.Wrap(err, "Argument read error")
	}
	h.s3CopyReq = &apimodels.S3CopyRequest{}
	if err = json.Unmarshal(b, h.s3CopyReq); err != nil {
		return errors.Wrap(err, "API error while unmarshalling JSON")
	}

	return nil
}

func (h *s3CopyHandler) Run(ctx context.Context) gimlet.Responder {
	h.count = h.count + 1
	grip.Errorf("ChayaMTesting 1 start of run. count: '%s', task:  '%s'", h.count, h.taskID)
	task, err := h.sc.FindTaskById(h.taskID)
	if err != nil {
		return gimlet.MakeJSONErrorResponder(err)
	}

	// START

	// Get the version for this task, so we can check if it has
	// any already-done pushes
	v, err := h.sc.FindVersionById(task.Version)
	if err != nil {
		return gimlet.MakeJSONInternalErrorResponder(errors.Wrapf(err, "problem querying task %s with version id %s",
			task.Id, task.Version))
	}

	// Check for an already-pushed file with this same file path,
	// but from a conflicting or newer commit sequence num
	if v == nil {
		return gimlet.MakeJSONErrorResponder(gimlet.ErrorResponse{
			StatusCode: http.StatusNotFound,
			Message:    fmt.Sprintf("no version found for build '%s'", task.BuildId),
		})
	}
	s3CopyReq := h.s3CopyReq
	copyFromLocation := strings.Join([]string{s3CopyReq.S3SourceBucket, s3CopyReq.S3SourcePath}, "/")
	copyToLocation := strings.Join([]string{s3CopyReq.S3DestinationBucket, s3CopyReq.S3DestinationPath}, "/")

	grip.Error(message.Fields{
		"message":               "ChayaMTesting pushlog 1",
		"copyToLocation":        copyToLocation,
		"v.RevisionOrderNumber": v.RevisionOrderNumber,
	})
	newestPushLog, err := model.FindPushLogAt(copyToLocation, v.RevisionOrderNumber)
	grip.Error(message.Fields{
		"message":               "ChayaMTesting pushlog 2",
		"copyToLocation":        copyToLocation,
		"v.RevisionOrderNumber": v.RevisionOrderNumber,
		"newestPushLog":         newestPushLog,
		"err":                   err,
	})

	if err != nil {
		return gimlet.MakeJSONInternalErrorResponder(errors.Wrapf(err, "problem querying for push log at %s (build=%s)",
			copyToLocation, task.BuildId))
	}
	grip.Errorf("ChayaMTesting 2 before checking for noop. count: '%s', task:  '%s'", h.count, h.taskID)

	if newestPushLog != nil {
		grip.Warningln("conflict with existing pushed file:", copyToLocation)
		return gimlet.NewJSONResponse(gimlet.ErrorResponse{
			StatusCode: http.StatusOK,
			Message:    fmt.Sprintf("noop, this version is currently in the process of trying to push, or has already succeeded in pushing the file: '%s'", copyToLocation),
		})
	}

	// It's now safe to put the file in its permanent location.
	newPushLog := model.NewPushLog(v, task, copyToLocation)
	if err = newPushLog.Insert(); err != nil {
		return gimlet.MakeJSONInternalErrorResponder(errors.Wrapf(err, "failed to create new push log: %+v", newPushLog))
	}

	// Now copy the file into the permanent location
	client := utility.GetHTTPClient()
	client.Timeout = 10 * time.Minute
	defer utility.PutHTTPClient(client)
	srcOpts := pail.S3Options{
		Credentials: pail.CreateAWSCredentials(s3CopyReq.AwsKey, s3CopyReq.AwsSecret, ""),
		Region:      s3CopyReq.S3SourceRegion,
		Name:        s3CopyReq.S3SourceBucket,
		Permissions: pail.S3Permissions(s3CopyReq.S3Permissions),
	}
	srcBucket, err := pail.NewS3MultiPartBucket(srcOpts)
	if err != nil {
		grip.Error(errors.Wrap(err, "S3 copy failed, could not establish connection to source bucket"))
	}
	destOpts := pail.S3Options{
		Credentials: pail.CreateAWSCredentials(s3CopyReq.AwsKey, s3CopyReq.AwsSecret, ""),
		Region:      s3CopyReq.S3DestinationRegion,
		Name:        s3CopyReq.S3DestinationBucket,
		Permissions: pail.S3Permissions(s3CopyReq.S3Permissions),
	}
	destBucket, err := pail.NewS3MultiPartBucket(destOpts)
	if err != nil {
		grip.Error(errors.Wrap(err, "S3 copy failed, could not establish connection to destination bucket"))
	}

	grip.Infof("performing S3 copy: '%s' => '%s'", copyFromLocation, copyToLocation)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	grip.Errorf("ChayaMTesting 3 starting retry. count: '%s', task:  '%s'", h.count, h.taskID)

	// err = utility.Retry(
	// 	ctx,
	// 	func() (bool, error) {
	copyOpts := pail.CopyOptions{
		SourceKey:         s3CopyReq.S3SourcePath,
		DestinationKey:    s3CopyReq.S3DestinationPath,
		DestinationBucket: destBucket,
	}
	err = srcBucket.Copy(ctx, copyOpts)
	if err != nil {
		grip.Errorf("ChayaMTesting  4 S3 copy failed for retrying. err:  %+v count: '%s', task:  '%s'", err, h.count, h.taskID)
		return gimlet.NewJSONInternalErrorResponse(gimlet.ErrorResponse{
			Message: fmt.Sprintf("55 S3 copy failed for task: '%s', \n error: '%s'", task.Id, err.Error()),
		})
	}

	err = errors.Wrapf(newPushLog.UpdateStatus(model.PushLogSuccess),
		"updating pushlog status failed for task %s", task.Id)

	grip.Error(err)

	// 	return false, err
	// }, utility.RetryOptions{
	// 	MaxAttempts: s3CopyAttempts,
	// 	MinDelay:    s3CopyRetryMinDelay,
	// })

	grip.Errorf("ChayaMTesting 5 before returning err: %+v count: '%s', task:  '%s'", err, h.count, h.taskID)
	// return gimlet.MakeJSONInternalErrorResponder(err)
	if err != nil {
		grip.Error(errors.Wrap(errors.WithStack(newPushLog.UpdateStatus(model.PushLogFailed)), "updating pushlog status failed"))

		return gimlet.NewJSONInternalErrorResponse(gimlet.ErrorResponse{
			Message: fmt.Sprintf("S3 copy failed for task: '%s', \n error: '%s'", task.Id, err.Error()),
		})
		// return  gimlet.MakeJSONErrorResponder(gimlet.ErrorResponse{
		// 	StatusCode: http.StatusBadRequest,
		// 	Message:    err.Error(),
		// })

		// return gimlet.MakeJSONInternalErrorResponder(errors.Wrap(err, "Database error"))
		// return gimlet.MakeJSONInternalErrorResponder(errors.Wrapf(err, "S3 copy failed for task %s", task.Id))
	}
	return gimlet.NewJSONResponse("S3 copy Successful")
}
