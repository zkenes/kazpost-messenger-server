// Copyright (c) 2017-present TinkerTech, Inc. All Rights Reserved.
// See License.txt for license information.

package jobs

import (
	"github.com/mattermost/mattermost-server/model"
)

type DataRetentionJobInterface interface {
	MakeWorker() model.Worker
	MakeScheduler() model.Scheduler
}
