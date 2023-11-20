package graphql

// This file will be automatically regenerated based on the schema, any resolver implementations
// will be copied through when generating and any unknown code will be moved to the end.
// Code generated by github.com/99designs/gqlgen version v0.17.40

import (
	"context"

	restModel "github.com/evergreen-ci/evergreen/rest/model"
	"github.com/evergreen-ci/evergreen/thirdparty"
)

// JiraTicket is the resolver for the jiraTicket field.
func (r *issueLinkResolver) JiraTicket(ctx context.Context, obj *restModel.APIIssueLink) (*thirdparty.JiraTicket, error) {
	return restModel.GetJiraTicketFromURL(*obj.URL)
}

// IssueLink returns IssueLinkResolver implementation.
func (r *Resolver) IssueLink() IssueLinkResolver { return &issueLinkResolver{r} }

type issueLinkResolver struct{ *Resolver }
