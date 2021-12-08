package model

import (
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/db"
	mgobson "github.com/evergreen-ci/evergreen/db/mgo/bson"
	"github.com/evergreen-ci/evergreen/model/task"
	"github.com/mongodb/anser/bsonutil"
	adb "github.com/mongodb/anser/db"
	"github.com/mongodb/grip"
	"github.com/mongodb/grip/message"
	"go.mongodb.org/mongo-driver/bson"
)

const (
	PushlogCollection = "pushes"
	PushLogSuccess    = "success"
	PushLogFailed     = "failed"
)

type PushLog struct {
	Id mgobson.ObjectId `bson:"_id,omitempty"`

	//the permanent location of the pushed file.
	Location string `bson:"location"`

	//the task id of the push stage
	TaskId string `bson:"task_id"`

	CreateTime time.Time `bson:"create_time"`
	Revision   string    `bson:"githash"`
	Status     string    `bson:"status"`

	//copied from version for the task
	RevisionOrderNumber int `bson:"order"`
}

var (
	// bson fields for the push log struct
	PushLogIdKey         = bsonutil.MustHaveTag(PushLog{}, "Id")
	PushLogLocationKey   = bsonutil.MustHaveTag(PushLog{}, "Location")
	PushLogTaskIdKey     = bsonutil.MustHaveTag(PushLog{}, "TaskId")
	PushLogCreateTimeKey = bsonutil.MustHaveTag(PushLog{}, "CreateTime")
	PushLogRevisionKey   = bsonutil.MustHaveTag(PushLog{}, "Revision")
	PushLogStatusKey     = bsonutil.MustHaveTag(PushLog{}, "Status")
	PushLogRonKey        = bsonutil.MustHaveTag(PushLog{}, "RevisionOrderNumber")
)

func NewPushLog(v *Version, task *task.Task, location string) *PushLog {
	return &PushLog{
		Id:                  mgobson.NewObjectId(),
		Location:            location,
		TaskId:              task.Id,
		CreateTime:          time.Now(),
		Revision:            v.Revision,
		RevisionOrderNumber: v.RevisionOrderNumber,
		Status:              evergreen.PushLogPushing,
	}
}

func (self *PushLog) Insert() error {
	return db.Insert(PushlogCollection, self)
}

func (self *PushLog) UpdateStatus(newStatus string) error {
	return db.Update(
		PushlogCollection,
		bson.M{
			PushLogIdKey: self.Id,
		},
		bson.M{
			"$set": bson.M{
				PushLogStatusKey: newStatus,
			},
		},
	)
}

func FindOnePushLog(query interface{}, projection interface{},
	sort []string) (*PushLog, error) {
	pushLog := &PushLog{}
	q := db.Query(query).Project(projection).Sort(sort)
	err := db.FindOneQ(PushlogCollection, q, pushLog)
	grip.Error(message.Fields{
		"message":    "ChayaMTesting pushlog 4",
		"query":      query,
		"q":          q,
		"projection": projection,
		"sort":       sort,
		"pushLog":    pushLog,
		"err":        err,
	})
	if adb.ResultsNotFound(err) || pushLog.Id == mgobson.ObjectId("") {
		return nil, nil
	}
	return pushLog, err
}

// FindNewerPushLog returns a PushLog item if there is a file pushed from
// this version that is in progress or has failed.
func FindPushLogAt(fileLoc string, revisionOrderNumber int) (*PushLog, error) {
	query := bson.M{
		PushLogStatusKey: bson.M{
			"$in": []string{
				evergreen.PushLogPushing, evergreen.PushLogSuccess,
			},
		},
		PushLogLocationKey: fileLoc,
		PushLogRonKey:      revisionOrderNumber,
	}
	grip.Error(message.Fields{
		"message":             "ChayaMTesting pushlog 3",
		"query":               query,
		"revisionOrderNumber": revisionOrderNumber,
		"fileLoc":             fileLoc,
	})
	existingPushLog, err := FindOnePushLog(
		query,
		db.NoProjection,
		[]string{"-" + PushLogRonKey},
	)
	grip.Error(message.Fields{
		"message":             "ChayaMTesting pushlog 5",
		"query":               query,
		"revisionOrderNumber": revisionOrderNumber,
		"fileLoc":             fileLoc,
		"existingPushLog":     existingPushLog,
		"err":                 err,
	})
	if err != nil {
		return nil, err
	}
	return existingPushLog, nil
}
