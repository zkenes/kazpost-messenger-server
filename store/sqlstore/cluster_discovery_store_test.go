// Copyright (c) 2015-present TinkerTech, Inc. All Rights Reserved.
// See License.txt for license information.

package sqlstore

import (
	"testing"

	"github.com/mattermost/mattermost-server/store/storetest"
)

func TestClusterDiscoveryStore(t *testing.T) {
	StoreTest(t, storetest.TestClusterDiscoveryStore)
}
