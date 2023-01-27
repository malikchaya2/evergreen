package route

import (
	"context"
	"fmt"
	"net/http"

	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/model/annotations"
	"github.com/evergreen-ci/evergreen/model/task"
	restModel "github.com/evergreen-ci/evergreen/rest/model"
	"github.com/evergreen-ci/gimlet"
	"github.com/evergreen-ci/utility"
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
	return getVersionBuildsAndTasks(taskIds, h.fetchAllExecutions)
}

func getVersionBuildsAndTasks(id string) gimlet.Responder {

	version, err := model.VersionFindOne(model.VersionById(id))
	if err != nil {
		return gimlet.NewJSONInternalErrorResponse(errors.Wrap(err, "finding version"))
	}
	apiVersion := restModel.APIVersion{}
	apiVersion.BuildFromService(*version)

	if apiVersion.IsPatchRequester() && !utility.FromBoolPtr(apiVersion.Activated) {
		return gimlet.NewJSONResponse("")
	}
	groupedBuildVariants, err := generateBuildVariants(utility.FromStringPtr(obj.Id), options, utility.FromStringPtr(obj.Requester))
	if err != nil {
		return nil, InternalServerError.Send(ctx, fmt.Sprintf("Error generating build variants for version %s : %s", *obj.Id, err.Error()))
	}
	return groupedBuildVariants, nil

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
