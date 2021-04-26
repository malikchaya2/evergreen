package data

import (
	"fmt"
	"net/http"
	"time"

	"github.com/evergreen-ci/evergreen/model/event"
	"github.com/evergreen-ci/evergreen/model/patch"
	restModel "github.com/evergreen-ci/evergreen/rest/model"
	"github.com/evergreen-ci/evergreen/trigger"
	"github.com/evergreen-ci/gimlet"
	"github.com/evergreen-ci/utility"
	"github.com/mongodb/grip"
	"github.com/mongodb/grip/message"
	"github.com/pkg/errors"
)

type DBSubscriptionConnector struct{}

func (dc *DBSubscriptionConnector) SaveSubscriptions(owner string, subscriptions []restModel.APISubscription) error {
	dbSubscriptions := []event.Subscription{}
	for _, subscription := range subscriptions {
		//here ...
		//maybe here, add the children somehow
		// add more logging to figure out
		subscriptionInterface, err := subscription.ToService()
		if err != nil {
			return gimlet.ErrorResponse{
				StatusCode: http.StatusBadRequest,
				Message:    "Error parsing request body: " + err.Error(),
			}
		}
		dbSubscription, ok := subscriptionInterface.(event.Subscription)
		dbSubscription.Subscriber.SubType = "test"
		if !ok {
			return gimlet.ErrorResponse{
				StatusCode: http.StatusInternalServerError,
				Message:    "Error parsing subscription interface",
			}
		}

		if !trigger.ValidateTrigger(dbSubscription.ResourceType, dbSubscription.Trigger) {
			return gimlet.ErrorResponse{
				StatusCode: http.StatusBadRequest,
				Message:    fmt.Sprintf("subscription type/trigger is invalid: %s/%s", dbSubscription.ResourceType, dbSubscription.Trigger),
			}
		}

		grip.Info(message.Fields{
			"message":               "ChayaMTesting rest/data/subscription.go 74",
			"message.NewStack":      message.NewStack(1, "stack"),
			"dbSubscriptions":       dbSubscriptions,
			"subscription":          subscription,
			"subscriptionInterface": subscriptionInterface,
			"dbSubscription":        dbSubscription,
		})

		if dbSubscription.OwnerType == event.OwnerTypePerson && dbSubscription.Owner == "" {
			dbSubscription.Owner = owner // default the current user
		}

		if dbSubscription.OwnerType == event.OwnerTypePerson && dbSubscription.Owner != owner {
			return gimlet.ErrorResponse{
				StatusCode: http.StatusUnauthorized,
				Message:    "Cannot change subscriptions for anyone other than yourself",
			}
		}

		if ok, msg := event.IsSubscriptionAllowed(dbSubscription); !ok {
			return gimlet.ErrorResponse{
				StatusCode: http.StatusBadRequest,
				Message:    msg,
			}
		}

		if ok, msg := event.ValidateSelectors(dbSubscription.Subscriber, dbSubscription.Selectors); !ok {
			return gimlet.ErrorResponse{
				StatusCode: http.StatusBadRequest,
				Message:    fmt.Sprintf("Invalid selectors: %s", msg),
			}
		}
		if ok, msg := event.ValidateSelectors(dbSubscription.Subscriber, dbSubscription.RegexSelectors); !ok {
			return gimlet.ErrorResponse{
				StatusCode: http.StatusBadRequest,
				Message:    fmt.Sprintf("Invalid regex selectors: %s", msg),
			}
		}

		err = dbSubscription.Validate()
		if err != nil {
			return gimlet.ErrorResponse{
				StatusCode: http.StatusBadRequest,
				Message:    "Error validating subscription: " + err.Error(),
			}
		}

		// ******* todo **********: remove this
		// if it's a parent, version, slack subscription add a subscription for all children
		// if dbSubscription.Subscriber.Type == event.SlackSubscriberType && dbSubscription.ResourceType == event.ResourceTypeVersion {
		if dbSubscription.ResourceType == event.ResourceTypeVersion {
			// todo:
			// find where these are proccessed, and do the whole waiting on children thing if subType is waitonchildren -- maybe in the payload
			// then if this works, edit the patches, pr subscriber to use subtype as well.

			//find all children, itterate through them
			var versionId string
			for _, selector := range dbSubscription.Selectors {
				if selector.Type == "id" {
					versionId = selector.Data
				}
			}
			children, err := getVersionChildren(versionId)
			if err != nil {
				return gimlet.ErrorResponse{
					StatusCode: http.StatusInternalServerError,
					Message:    "Error retrieving child versions: " + err.Error(),
				}
			}
			if len(children) != 0 {
				dbSubscription.Subscriber.SubType = event.Parent
				dbSubscriptions = append(dbSubscriptions, dbSubscription)
			} else {
				dbSubscriptions = append(dbSubscriptions, dbSubscription)
			}

			for _, childPatchId := range children {
				grip.Info(message.Fields{
					"message":      "ChayaMTesting rest/data/subscription.go 129",
					"children":     children,
					"childPatchId": childPatchId,
				})
				childDbSubscription := dbSubscription
				childDbSubscription.LastUpdated = time.Now()
				var selectors []event.Selector
				for _, selector := range dbSubscription.Selectors {
					grip.Info(message.Fields{
						"message":             "ChayaMTesting rest/data/subscription.go 164",
						"selector":            selector,
						"selector.Type":       selector.Type,
						"selector.Type == id": selector.Type == "id",
						"selector.Data":       selector.Data,
					})
					if selector.Type == "id" {
						selector.Data = childPatchId
					}
					selectors = append(selectors, selector)
				}
				childDbSubscription.Selectors = selectors
				childDbSubscription.Subscriber.SubType = event.Child
				dbSubscriptions = append(dbSubscriptions, childDbSubscription)
				grip.Info(message.Fields{
					"message":             "ChayaMTesting rest/data/subscription.go 164",
					"message.NewStack":    message.NewStack(1, "stack"),
					"dbSubscriptions":     dbSubscriptions,
					"childDbSubscription": childDbSubscription,
				})
			}
		} else {
			dbSubscriptions = append(dbSubscriptions, dbSubscription)
		}

	}

	catcher := grip.NewSimpleCatcher()
	for _, subscription := range dbSubscriptions {
		grip.Info(message.Fields{
			"message":          "ChayaMTesting rest/data/subscription.go 179",
			"message.NewStack": message.NewStack(1, "stack"),
			"dbSubscriptions":  dbSubscriptions,
			"subscription":     subscription,
		})
		catcher.Add(subscription.Upsert())
	}
	return catcher.Resolve()
}

func getVersionChildren(versionId string) ([]string, error) {
	patchDoc, err := patch.FindOne(patch.ByVersion(versionId))
	if err != nil {
		return nil, errors.Wrap(err, "error getting patch")
	}
	if patchDoc == nil {
		return nil, errors.Wrap(err, "patch not found")
	}
	return patchDoc.Triggers.ChildPatches, nil

}

func (dc *DBSubscriptionConnector) GetSubscriptions(owner string, ownerType event.OwnerType) ([]restModel.APISubscription, error) {
	if len(owner) == 0 {
		return nil, gimlet.ErrorResponse{
			StatusCode: http.StatusBadRequest,
			Message:    "no subscription owner provided",
		}
	}

	subs, err := event.FindSubscriptionsByOwner(owner, ownerType)
	if err != nil {
		return nil, errors.Wrap(err, "failed to fetch subscriptions")
	}

	apiSubs := make([]restModel.APISubscription, len(subs))

	for i := range subs {
		err = apiSubs[i].BuildFromService(subs[i])
		if err != nil {
			return nil, errors.Wrap(err, "failed to marshal subscriptions")
		}
	}

	return apiSubs, nil
}

func (dc *DBSubscriptionConnector) DeleteSubscriptions(owner string, ids []string) error {
	for _, id := range ids {
		subscription, err := event.FindSubscriptionByID(id)
		if err != nil {
			return gimlet.ErrorResponse{
				StatusCode: http.StatusInternalServerError,
				Message:    err.Error(),
			}
		}
		if subscription == nil {
			return gimlet.ErrorResponse{
				StatusCode: http.StatusNotFound,
				Message:    "Subscription not found",
			}
		}
		if subscription.Owner != owner {
			return gimlet.ErrorResponse{
				StatusCode: http.StatusUnauthorized,
				Message:    "Cannot delete subscriptions for someone other than yourself",
			}
		}
	}

	catcher := grip.NewBasicCatcher()
	for _, id := range ids {
		catcher.Add(event.RemoveSubscription(id))
	}
	return catcher.Resolve()
}

func (dc *DBSubscriptionConnector) CopyProjectSubscriptions(oldProject, newProject string) error {
	subs, err := event.FindSubscriptionsByOwner(oldProject, event.OwnerTypeProject)
	if err != nil {
		return errors.Wrapf(err, "error finding subscription for project '%s'", oldProject)
	}

	catcher := grip.NewBasicCatcher()
	for _, sub := range subs {
		sub.Owner = newProject
		sub.ID = ""
		catcher.Add(sub.Upsert())
	}
	return catcher.Resolve()
}

type MockSubscriptionConnector struct {
	MockSubscriptions []restModel.APISubscription
}

func (mc *MockSubscriptionConnector) GetSubscriptions(owner string, ownerType event.OwnerType) ([]restModel.APISubscription, error) {
	return mc.MockSubscriptions, nil
}

func (mc *MockSubscriptionConnector) SaveSubscriptions(owner string, subscriptions []restModel.APISubscription) error {
	for _, sub := range subscriptions {
		mc.MockSubscriptions = append(mc.MockSubscriptions, sub)
	}
	return nil
}

func (mc *MockSubscriptionConnector) DeleteSubscriptions(owner string, ids []string) error {
	idMap := make(map[string]bool)
	for _, id := range ids {
		idMap[id] = true
	}

	n := 0
	for _, sub := range mc.MockSubscriptions {
		if idMap[utility.FromStringPtr(sub.ID)] {
			mc.MockSubscriptions[n] = sub
			n++
		}
	}
	mc.MockSubscriptions = mc.MockSubscriptions[:n]

	return nil
}

func (mc *MockSubscriptionConnector) CopyProjectSubscriptions(oldProject, newProject string) error {
	return nil
}
