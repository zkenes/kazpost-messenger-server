// Copyright (c) 2017 Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

import {connect} from 'react-redux';
import {createSelector} from 'reselect';
import {getCustomEmojisByName} from 'mattermost-redux/selectors/entities/emojis';
import {getBool} from 'mattermost-redux/selectors/entities/preferences';
import {getCurrentUserMentionKeys, getUsersByUsername} from 'mattermost-redux/selectors/entities/users';

import {getCurrentTeam} from 'mattermost-redux/selectors/entities/teams';

import {Preferences} from 'mattermost-redux/constants';
import {getSiteURL} from 'utils/url.jsx';

import {EmojiMap} from 'stores/emoji_store.jsx';

import PostMessageView from './post_message_view.jsx';

const getEmojiMap = createSelector(
    getCustomEmojisByName,
    (customEmojis) => {
        return new EmojiMap(customEmojis);
    }
);

function mapStateToProps(state, ownProps) {
    return {
        ...ownProps,
        emojis: getEmojiMap(state),
        enableFormatting: getBool(state, Preferences.CATEGORY_ADVANCED_SETTINGS, 'formatting', true),
        mentionKeys: getCurrentUserMentionKeys(state),
        usernameMap: getUsersByUsername(state),
        team: getCurrentTeam(state),
        siteUrl: getSiteURL(),
        components: state.plugins.components
    };
}

export default connect(mapStateToProps)(PostMessageView);
