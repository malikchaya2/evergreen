package route

import (
	"context"
	"net/http"

	"github.com/evergreen-ci/evergreen/model"
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
	buildVariants, err := getVersionBuildsAndTasks(h.versionId)
	if err != nil {
		return gimlet.MakeJSONErrorResponder(errors.Wrap(err, "error getting version builds and tasks"))
	}
	return gimlet.NewJSONResponse(buildVariants)
}

func getVersionBuildsAndTasks(id string) ([]*model.GroupedBuildVariant, error) {

	version, err := model.VersionFindOne(model.VersionById(id))
	if err != nil {
		return nil, errors.Wrap(err, "finding version")
	}
	apiVersion := restModel.APIVersion{}
	apiVersion.BuildFromService(*version)

	if apiVersion.IsPatchRequester() && !utility.FromBoolPtr(apiVersion.Activated) {
		return nil, nil
	}
	return model.GenerateBuildVariants(id, options, utility.FromStringPtr(&version.Requester))

}
