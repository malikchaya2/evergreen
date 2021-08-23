package trigger

import (
	"fmt"
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/model/event"
	"github.com/evergreen-ci/evergreen/model/notification"
	"github.com/evergreen-ci/evergreen/model/patch"
	"github.com/evergreen-ci/evergreen/model/task"
	restModel "github.com/evergreen-ci/evergreen/rest/model"
	"github.com/evergreen-ci/utility"
	"github.com/mongodb/grip"
	"github.com/mongodb/grip/message"
	"github.com/pkg/errors"
	mgobson "gopkg.in/mgo.v2/bson"
)

type patchTriggers struct {
	event    *event.EventLogEntry
	data     *event.PatchEventData
	patch    *patch.Patch
	uiConfig evergreen.UIConfig

	base
}

func makePatchTriggers() eventHandler {
	t := &patchTriggers{}
	t.base.triggers = map[string]trigger{
		event.TriggerOutcome:      t.patchOutcome,
		event.TriggerFailure:      t.patchFailure,
		event.TriggerSuccess:      t.patchSuccess,
		event.TriggerPatchStarted: t.patchStarted,
	}
	return t
}

func (t *patchTriggers) Fetch(e *event.EventLogEntry) error {
	var err error
	if err = t.uiConfig.Get(evergreen.GetEnvironment()); err != nil {
		return errors.Wrap(err, "Failed to fetch ui config")
	}

	oid := mgobson.ObjectIdHex(e.ResourceId)

	t.patch, err = patch.FindOne(patch.ById(oid))
	if err != nil {
		return errors.Wrapf(err, "failed to fetch patch '%s'", e.ResourceId)
	}
	if t.patch == nil {
		return errors.Errorf("can't find patch '%s'", e.ResourceId)
	}
	var ok bool
	t.data, ok = e.Data.(*event.PatchEventData)
	if !ok {
		return errors.Errorf("patch '%s' contains unexpected data with type '%T'", e.ResourceId, e.Data)
	}
	t.event = e

	return nil
}

func (t *patchTriggers) Selectors() []event.Selector {
	return []event.Selector{
		{
			Type: event.SelectorID,
			Data: t.patch.Id.Hex(),
		},
		{
			Type: event.SelectorObject,
			Data: event.ObjectPatch,
		},
		{
			Type: event.SelectorProject,
			Data: t.patch.Project,
		},
		{
			Type: event.SelectorOwner,
			Data: t.patch.Author,
		},
		{
			Type: event.SelectorStatus,
			Data: t.patch.Status,
		},
	}
}

func (t *patchTriggers) patchOutcome(sub *event.Subscription) (*notification.Notification, error) {
	grip.Info(message.Fields{
		"message":       "chayaMTesting patch.go  patchOutcome 93",
		"t.data.Status": t.data.Status,
		"sub":           sub,
		"t.patch":       t.patch,
	})
	if t.data.Status != evergreen.PatchSucceeded && t.data.Status != evergreen.PatchFailed {
		return nil, nil
	}

	if sub.Subscriber.Type == event.RunChildPatchSubscriberType {
		target, ok := sub.Subscriber.Target.(*event.ChildPatchSubscriber)
		if !ok {
			return nil, errors.Errorf("target '%s' didn't not have expected type", sub.Subscriber.Target)
		}
		ps := target.ParentStatus

		if ps != evergreen.PatchSucceeded && ps != evergreen.PatchFailed && ps != evergreen.PatchAllOutcomes {
			return nil, nil
		}

		successOutcome := (ps == evergreen.PatchSucceeded) && (t.data.Status == evergreen.PatchSucceeded)
		failureOutcome := (ps == evergreen.PatchFailed) && (t.data.Status == evergreen.PatchFailed)
		anyOutcome := (ps == evergreen.PatchAllOutcomes)

		if successOutcome || failureOutcome || anyOutcome {
			err := finalizeChildPatch(sub)

			if err != nil {
				return nil, errors.Wrap(err, "Failed to finalize child patch")
			}
			return nil, nil
		}
	}

	isReady, err := t.waitOnChildrenOrSiblings(sub)
	grip.Info(message.Fields{
		"message": "chayaMTesting patch.go patchOutcome 128",
		"isReady": isReady,
		"err":     err,
		"t.patch": t.patch,
		"sub":     sub,
	})
	if err != nil {
		return nil, err
	}
	if !isReady {
		return nil, nil
	}

	grip.Info(message.Fields{
		"message":       "chayaMTesting patch.go patchOutcome 142",
		"t.data.Status": t.data.Status,
		"sub":           sub,
		"t.patch":       t.patch,
		"isReady":       isReady,
		"err":           err,
	})
	return t.generate(sub)
}

// the problem: the child doesn't have the parent patch
func (t *patchTriggers) waitOnChildrenOrSiblings(sub *event.Subscription) (bool, error) {
	grip.Info(message.Fields{
		"message":             "chayaMTesting patch.go waitOnChildrenOrSiblings 134",
		"sub.Subscriber.Type": sub.Subscriber.Type,
		"t.patch":             t.patch,
	})
	if sub.Subscriber.Type != event.GithubPullRequestSubscriberType {
		return true, nil
	}
	target, ok := sub.Subscriber.Target.(*event.GithubPullRequestSubscriber)
	grip.Info(message.Fields{
		"message": "chayaMTesting patch.go 142",
		"target":  target,
		"ok":      ok,
	})
	if !ok {
		return false, errors.Errorf("target '%s' didn't not have expected type", sub.Subscriber.Target)
	}
	subType := target.Type
	grip.Info(message.Fields{
		"message":             "chayaMTesting patch.go waitOnChildrenOrSiblings 151",
		"t.patch.IsParent() ": t.patch.IsParent(),
		"t.patch.IsChild()":   t.patch.IsChild(),
		"subType":             subType,
		"if check":            !(t.patch.IsParent() || (t.patch.IsChild() && subType == event.WaitOnChild)),
	})

	// notifications are only delayed if the patch is either a parent, or a child that is of subType event.WaitOnChild.
	// we don't always wait on siblings when it is a childpatch, since childpatches need to let github know when they
	// are done running so their status can be displayed to the user as they finish
	if !(t.patch.IsParent() || (t.patch.IsChild() && subType == event.WaitOnChild)) {
		return true, nil
	}
	// get the children or siblings to wait on
	isReady, parentPatch, isFailingStatus, err := checkPatchStatus(t.patch)
	//parent patch is null here and it's a child
	grip.Info(message.Fields{
		"message":           "chayaMTesting patch.go waitOnChildrenOrSiblings 168",
		"isReady":           isReady,
		"parentPatch":       parentPatch,
		"isFailingStatus":   isFailingStatus,
		"err":               err,
		"t.patch.IsChild()": t.patch.IsChild(),
	})
	if err != nil {
		return false, errors.Wrapf(err, "error getting patch status for '%s'", t.patch.Id)
	}

	if isFailingStatus {
		t.data.Status = evergreen.PatchFailed
	}

	if t.patch.IsChild() {
		// we want the subscription to be on the parent
		// now that the children are done, the parent can be considered done.
		t.patch = parentPatch
	}

	grip.Info(message.Fields{
		"message":           "chayaMTesting patch.go waitOnChildrenOrSiblings 189",
		"isReady":           isReady,
		"t.patch":           t.patch,
		"t.patch.IsChild()": t.patch.IsChild(),
	})
	return isReady, nil
}

func checkPatchStatus(p *patch.Patch) (bool, *patch.Patch, bool, error) {
	isReady := false
	childrenOrSiblings, parentPatch, err := p.GetPatchFamily()
	if err != nil {
		return isReady, nil, false, errors.Wrap(err, "error getting child or sibling patches")
	}
	// here
	// make sure the parent is done, if not, wait for the parent
	grip.Info(message.Fields{
		"message":            "chayaMTesting patch.go checkPatchStatus 230",
		"isReady":            isReady,
		"childrenOrSiblings": childrenOrSiblings,
		"parentPatch":        parentPatch,
		"err":                err,
		" p.IsChild()":       p.IsChild(),
		// "parentPatch.Status": parentPatch.Status,
	})
	if p.IsChild() {
		grip.Info(message.Fields{
			"message": "chayaMTesting patch.go checkPatchStatus 215",
			"evergreen.IsFinishedPatchStatus(parentPatch.Status)": evergreen.IsFinishedPatchStatus(parentPatch.Status),
		})
		if !evergreen.IsFinishedPatchStatus(parentPatch.Status) {
			grip.Info(message.Fields{
				"message":            "chayaMTesting patch.go 245, returning",
				"parentPatch.Status": parentPatch.Status,
				"isReady":            isReady,
			})
			return isReady, parentPatch, false, nil
		}
	}
	childrenStatus, err := getChildrenOrSiblingsReadiness(childrenOrSiblings)
	if err != nil {
		return isReady, nil, false, errors.Wrap(err, "error getting child or sibling information")
	}
	if !evergreen.IsFinishedPatchStatus(childrenStatus) {
		return isReady, nil, false, nil
	}
	isReady = true

	isFailingStatus := (p.Status == evergreen.PatchFailed)
	if childrenStatus == evergreen.PatchFailed || (p.IsChild() && parentPatch.Status == evergreen.PatchFailed) {
		isFailingStatus = true
	}
	grip.Info(message.Fields{
		"message":         "chayaMTesting patch.go checkPatchStatus 266",
		"isReady":         isReady,
		"parentPatch":     parentPatch,
		"isFailingStatus": isFailingStatus,
		"err":             err,
	})
	// over here: it's the parent, ready is true, failing status is true. but it stops here. doesn't go to the next line.
	return isReady, parentPatch, isFailingStatus, err

}

func (t *patchTriggers) patchFailure(sub *event.Subscription) (*notification.Notification, error) {
	if t.data.Status != evergreen.PatchFailed {
		return nil, nil
	}

	return t.generate(sub)
}

func getChildrenOrSiblingsReadiness(childrenOrSiblings []string) (string, error) {
	childrenStatus := evergreen.PatchSucceeded
	for _, childPatch := range childrenOrSiblings {
		childPatchDoc, err := patch.FindOneId(childPatch)
		if err != nil {
			return "", errors.Wrapf(err, "error getting tasks for child patch '%s'", childPatch)
		}
		if childPatchDoc == nil {
			return "", errors.Errorf("child patch '%s' not found", childPatch)
		}
		if childPatchDoc.Status == evergreen.PatchFailed {
			childrenStatus = evergreen.PatchFailed
		}
		if !evergreen.IsFinishedPatchStatus(childPatchDoc.Status) {
			return childPatchDoc.Status, nil
		}
	}
	return childrenStatus, nil

}

func finalizeChildPatch(sub *event.Subscription) error {
	target, ok := sub.Subscriber.Target.(*event.ChildPatchSubscriber)
	if !ok {
		return errors.Errorf("target '%s' didn't not have expected type", sub.Subscriber.Target)
	}
	childPatch, err := patch.FindOneId(target.ChildPatchId)
	if err != nil {
		return errors.Wrap(err, "Failed to fetch child patch")
	}
	if childPatch == nil {
		return errors.Wrap(err, "child patch not found")
	}
	conf, err := evergreen.GetConfig()
	if err != nil {
		return errors.Wrap(err, "can't get evergreen configuration")
	}

	ghToken, err := conf.GetGithubOauthToken()
	if err != nil {
		return errors.Wrap(err, "can't get Github OAuth token from configuration")
	}

	ctx, cancel := evergreen.GetEnvironment().Context()
	defer cancel()

	if _, err := model.FinalizePatch(ctx, childPatch, target.Requester, ghToken); err != nil {
		grip.Error(message.WrapError(err, message.Fields{
			"message":       "Failed to finalize patch document",
			"source":        target.Requester,
			"patch_id":      childPatch.Id,
			"variants":      childPatch.BuildVariants,
			"tasks":         childPatch.Tasks,
			"variant_tasks": childPatch.VariantsTasks,
			"alias":         childPatch.Alias,
		}))
		return err
	}
	return nil
}

func (t *patchTriggers) patchSuccess(sub *event.Subscription) (*notification.Notification, error) {
	if t.data.Status != evergreen.PatchSucceeded {
		return nil, nil
	}

	return t.generate(sub)
}

func (t *patchTriggers) patchStarted(sub *event.Subscription) (*notification.Notification, error) {
	if t.data.Status != evergreen.PatchStarted {
		return nil, nil
	}

	return t.generate(sub)
}

func (t *patchTriggers) makeData(sub *event.Subscription) (*commonTemplateData, error) {
	api := restModel.APIPatch{}
	if err := api.BuildFromService(*t.patch); err != nil {
		return nil, errors.Wrap(err, "error building json model")
	}
	projectName := t.patch.Project
	if api.ProjectIdentifier != nil {
		projectName = utility.FromStringPtr(api.ProjectIdentifier)
	}

	data := commonTemplateData{
		ID:                t.patch.Id.Hex(),
		EventID:           t.event.ID,
		SubscriptionID:    sub.ID,
		DisplayName:       t.patch.Id.Hex(),
		Description:       t.patch.Description,
		Object:            event.ObjectPatch,
		Project:           projectName,
		URL:               versionLink(t.uiConfig.Url, t.patch.Version, true),
		PastTenseStatus:   t.data.Status,
		apiModel:          &api,
		githubState:       message.GithubStatePending,
		githubDescription: "tasks are running",
	}

	if t.patch.IsChild() {
		githubContext, err := t.getGithubContext()
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get githubContext for '%s'", t.patch.Id)
		}
		data.githubContext = githubContext
	} else {
		data.githubContext = "evergreen"
	}

	slackColor := evergreenFailColor
	finishTime := t.patch.FinishTime
	if utility.IsZeroTime(finishTime) {
		finishTime = time.Now()
	}
	if t.data.Status == evergreen.PatchSucceeded {
		slackColor = evergreenSuccessColor
		data.githubState = message.GithubStateSuccess
		data.githubDescription = fmt.Sprintf("patch finished in %s", finishTime.Sub(t.patch.StartTime).String())
	} else if t.data.Status == evergreen.PatchFailed {
		data.githubState = message.GithubStateFailure
		data.githubDescription = fmt.Sprintf("patch finished in %s", finishTime.Sub(t.patch.StartTime).String())
	}
	if t.patch.IsGithubPRPatch() {
		data.slack = append(data.slack, message.SlackAttachment{
			Title:     "Github Pull Request",
			TitleLink: fmt.Sprintf("https://github.com/%s/%s/pull/%d#partial-pull-merging", t.patch.GithubPatchData.BaseOwner, t.patch.GithubPatchData.BaseRepo, t.patch.GithubPatchData.PRNumber),
			Color:     slackColor,
		})
	}
	var makespan time.Duration
	if utility.IsZeroTime(t.patch.FinishTime) {
		patchTasks, err := task.Find(task.ByVersion(t.patch.Id.Hex()))
		if err == nil {
			_, makespan = task.GetTimeSpent(patchTasks)
		}
	} else {
		makespan = t.patch.FinishTime.Sub(t.patch.StartTime)
	}

	data.slack = append(data.slack, message.SlackAttachment{
		Title:     "Evergreen Patch",
		TitleLink: data.URL,
		Text:      t.patch.Description,
		Color:     slackColor,
		Fields: []*message.SlackAttachmentField{
			{
				Title: "Time Taken",
				Value: makespan.String(),
			},
		},
	})
	return &data, nil
}

func (t *patchTriggers) generate(sub *event.Subscription) (*notification.Notification, error) {
	grip.Info(message.Fields{
		"message":             "chayaMTesting patch.go generate 444",
		"t.data.Status":       t.data.Status,
		"sub":                 sub,
		"t.patch":             t.patch,
		"t.patch.IsParent() ": t.patch.IsParent(),
		"t.patch.IsChild()":   t.patch.IsChild(),
	})
	data, err := t.makeData(sub)
	grip.Info(message.Fields{
		"message":             "chayaMTesting patch.go generate 444",
		"t.data.Status":       t.data.Status,
		"sub":                 sub,
		"t.patch":             t.patch,
		"t.patch.IsParent() ": t.patch.IsParent(),
		"t.patch.IsChild()":   t.patch.IsChild(),
		"data":                data,
		"err":                 err,
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to collect patch data")
	}

	payload, err := makeCommonPayload(sub, t.Selectors(), data)
	grip.Info(message.Fields{
		"message":             "chayaMTesting patch.go generate 444",
		"t.data.Status":       t.data.Status,
		"sub":                 sub,
		"t.patch":             t.patch,
		"t.patch.IsParent() ": t.patch.IsParent(),
		"t.patch.IsChild()":   t.patch.IsChild(),
		"payload":             payload,
		"err":                 err,
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to build notification")
	}
	grip.Info(message.Fields{
		"message":             "chayaMTesting patch.go generate 444",
		"t.data.Status":       t.data.Status,
		"sub":                 sub,
		"t.patch":             t.patch,
		"t.patch.IsParent() ": t.patch.IsParent(),
		"t.patch.IsChild()":   t.patch.IsChild(),
		"data":                data,
		"err":                 err,
		"t.event.ID":          t.event.ID,
		"sub.Trigger":         sub.Trigger,
		"&sub.Subscriber":     &sub.Subscriber,
		"payload":             payload,
	})
	return notification.New(t.event.ID, sub.Trigger, &sub.Subscriber, payload)
}

func (t *patchTriggers) getGithubContext() (string, error) {
	projectIdentifier, err := model.GetIdentifierForProject(t.patch.Project)
	if err != nil { // default to ID
		projectIdentifier = t.patch.Project
	}

	parentPatch, err := patch.FindOneId(t.patch.Triggers.ParentPatch)
	if err != nil {
		return "", errors.Wrap(err, "can't get parent patch")
	}
	if parentPatch == nil {
		return "", errors.Errorf("parent patch '%s' does not exist", t.patch.Triggers.ParentPatch)
	}
	patchIndex, err := t.patch.GetPatchIndex(parentPatch)
	if err != nil {
		return "", errors.Wrap(err, "error getting child patch index")
	}
	var githubContext string
	if patchIndex == 0 || patchIndex == -1 {
		githubContext = fmt.Sprintf("evergreen/%s", projectIdentifier)
	} else {
		githubContext = fmt.Sprintf("evergreen/%s/%d", projectIdentifier, patchIndex)
	}
	return githubContext, nil
}
