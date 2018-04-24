// Copyright (c) 2018-present TinkerTech, Inc. All Rights Reserved.
// See License.txt for license information.

package sqlstore

import (
	"testing"

	"github.com/mattermost/mattermost-server/store/storetest"
)

func TestRoleStore(t *testing.T) {
	StoreTest(t, storetest.TestRoleStore)
}
