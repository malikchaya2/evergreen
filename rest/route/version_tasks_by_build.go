package route

import (
	"context"
	"net/http"

	"github.com/evergreen-ci/evergreen/model/annotations"
	"github.com/evergreen-ci/evergreen/model/task"
	restModel "github.com/evergreen-ci/evergreen/rest/model"
	"github.com/evergreen-ci/gimlet"
	"github.com/pkg/errors"
)

////////////////////////////////////////////////////////////////////////
//
// GET /rest/v2/versions/{version_id}/tasks_by_build

type makeVersionTasksByBuildHandler struct {
	versionId          string
	fetchAllExecutions bool
}

func makeVersionTasksByBuild() gimlet.RouteHandler {
	return &makeVersionTasksByBuildHandler{}
}

func (h *makeVersionTasksByBuildHandler) Factory() gimlet.RouteHandler {
	return &makeVersionTasksByBuildHandler{}
}

func (h *makeVersionTasksByBuildHandler) Parse(ctx context.Context, r *http.Request) error {
	h.versionId = gimlet.GetVars(r)["version_id"]
	if h.versionId == "" {
		return gimlet.ErrorResponse{
			Message:    "version ID cannot be empty",
			StatusCode: http.StatusBadRequest,
		}
	}

	h.fetchAllExecutions = r.URL.Query().Get("fetch_all_executions") == "true"
	return nil
}

func (h *makeVersionTasksByBuildHandler) Run(ctx context.Context) gimlet.Responder {
	//put this in a for loop
	taskIds, err := task.FindAllTaskIDsFromBuild(h.versionId)
	if err != nil {
		return gimlet.NewJSONInternalErrorResponse(errors.Wrapf(err, "finding task IDs for version '%s'", h.versionId))
	}
	return getBuildsAndTasks(taskIds, h.fetchAllExecutions)
}

func getBuildsAndTasks(taskIds []string, allExecutions bool) gimlet.Responder {
	allAnnotations, err := annotations.FindByTaskIds(taskIds)
	if err != nil {
		return gimlet.NewJSONInternalErrorResponse(errors.Wrap(err, "finding task annotations"))
	}
	annotationsToReturn := allAnnotations
	if !allExecutions {
		annotationsToReturn = annotations.GetLatestExecutions(allAnnotations)
	}
	var res []restModel.APITaskAnnotation
	for _, a := range annotationsToReturn {
		apiAnnotation := restModel.APITaskAnnotationBuildFromService(a)
		res = append(res, *apiAnnotation)
	}

	return gimlet.NewJSONResponse(res)
}
