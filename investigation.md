# getParentsPosts query investigation

## Terminology

For the purpose of this discussion, I've defined the following terms to hopefully match their use
within the code base.

* *post*: a message posted to a channel
* *root post*: a post not made in reply to any other post
* *reply post*: a post made in reply to another post
* *thread*: a set of posts including a root post and any reply posts
* *window*: a subset of a channel's posts, queried by offset and limit

## Overview

`getParentsPosts` is invoked as part of `GetPosts`, which itself accepts a `channelId`, `offset` 
and `limit` defining a window for the query. For the webapp, the typical window is the 30 most
recent posts for the given channel.

`GetPosts` first invokes `getRootPosts`. This method is somewhat of a misnomer, since reply posts
are visually interleaved with root posts, and this method fetches a window potentially containing 
both.

`GetPosts` then invokes `getParentsPosts`. This method, itself awkwardly named, fetches all threads
having posts in the same window. For example, if a root past was made last year, and received
100 reply posts, the most recent of which occurred in the current window, that root post and all 
100 reply posts are loaded by `getParentsPosts`, along with any other such threads.

This second query supplies a count of replies for display in the centre channel when hovering 
over a post. It also allows the RHS for a given thread to be opened without any additional 
network call.

## SQL Query

`getParentsPosts` runs the following SQL query:

    SELECT
        q2.*
    FROM
        Posts q2
    INNER JOIN (
        SELECT DISTINCT
            q3.RootId
        FROM (
            SELECT
                RootId
            FROM
                Posts
            WHERE
                ChannelId = :ChannelId
            AND DeleteAt = 0
            ORDER BY 
                CreateAt DESC
            LIMIT :Limit OFFSET :Offset
        ) q3
        WHERE q3.RootId != ''
    ) q1 ON (
        q1.RootId = q2.Id 
     OR q1.RootId = q2.RootId
    )
    WHERE
        ChannelId = :ChannelId
    AND DeleteAt = 0
    ORDER BY 
        CreateAt

Observe that the innermost query matches the `getRootPosts` query, except for fetching only the
root post ids in question. As this query occurs outside of a transaction, there is a possibility 
for the duplicate `getRootPosts` query to find a different set of root posts and potentially return 
incorrect results on a busy channel.

## Query Improvements

### Superfluous ORDER BY

The final `ORDER BY CreateAt` is superfluous. The results of `getParentsPosts` are added to a 
`PostList`:

    cpc := s.getParentsPosts(channelId, offset, limit)
    // ...
    cpr := <-cpc
    // ... 
    parents := cpr.Data.([]*model.Post)
    // ...
    for _, p := range parents {
            list.AddPost(p)
    }

but `list.AddPost` puts the post into a map indexed by `Id`, and the underlying sort order for
threads is discarded. (The order from `getRootPosts` is explicitly preserved.) Indeed, removing 
ordering randomly (`ORDER BY RAND()`) and then removing the `ORDER BY` altogether has no impact on 
the rendering of the RHS thread.

This is at best a minor improvement, given the result size of a typical query, but it does remove
a `Using temporary; Using filesort` in the query plan.

### Merging `getRootPosts` and `getParentsPosts`

As noted above, `getParentsPosts` effectively embeds `getRootPosts`. Since it fetches all threads
in the window, it also implicitly returns the same reply posts returned by `getRootPosts`. 

The posts returned by `getRootPosts` are processed as follows:

    rpc := s.getRootPosts(channelId, offset, limit)
    // ...
    rpr := <-rpc
    // ...
    posts := rpr.Data.([]*model.Post)
    // ...
    for _, p := range posts {
            list.AddPost(p)
            list.AddOrder(p.Id)
    }

This is almost exactly duplicates the handling of `getParentsPosts` above, except for recording
the order in which the root posts are returned. For completeness, the query run by `getRootPosts`
is:

    SELECT 
        * 
    FROM 
        Posts 
    WHERE 
        ChannelId = :ChannelId 
    AND DeleteAt = 0 
    ORDER BY 
        CreateAt DESC 
    LIMIT :Limit OFFSET :Offset

Merging these two queries (removing the superfluous `ORDER BY` as above), while retaining the 
underlying order:
   
    SELECT
        post.*,
        post.Id = centreChannelPost.Id AS is_centre_channel
    FROM (
        SELECT
            Id,
            RootId
        FROM
            Posts
        WHERE
            ChannelId = :ChannelId
        AND DeleteAt = 0
        ORDER BY 
            CreateAt DESC
        LIMIT :Limit OFFSET :Offset
    )
    JOIN Posts post ON (
        post.Id = centreChannelPost.Id
     OR post.RootId = centreChannelPost.Id
     OR (post.RootId != '' AND post.RootId = centreChannelPost.RootId)
    )
    WHERE
        post.DeleteAt = 0

The computed `IsCentreChannel` gives enough context to rebuild the `Order` array in the `PostList`:

    for _, p := range posts {
            list.AddPost(p)
            if post.IsCentreChannel
            list.AddOrder(p.Id)
    }


As it turns out, this is precisely the strategy used by `GetPostsSince`.





		    SELECT DISTINCT post.Id
		    FROM (
			SELECT
			    Id,
			    RootId
			FROM
			    Posts
			WHERE
			    ChannelId = "na7oxnzzbfd9pppud1q89trfze"
			AND DeleteAt = 0
			ORDER BY 
			    CreateAt DESC
			LIMIT 30 OFFSET 0
		    ) centreChannelPost
		    JOIN Posts post ON (
			post.Id = centreChannelPost.Id
		     OR post.RootId = centreChannelPost.Id
		    )
		    WHERE
			post.DeleteAt = 0
		     OR (post.RootId != '' AND post.RootId = centreChannelPost.RootId)



    (SELECT 
        Id
    FROM 
        Posts 
    WHERE 
        ChannelId = "na7oxnzzbfd9pppud1q89trfze" 
    AND DeleteAt = 0 
    ORDER BY 
        CreateAt DESC 
    LIMIT 30 OFFSET 0)

    UNION

    (SELECT
        Id
    FROM
        Posts q2
    INNER JOIN (
        SELECT DISTINCT
            q3.RootId
        FROM (
            SELECT
                RootId
            FROM
                Posts
            WHERE
                ChannelId = "na7oxnzzbfd9pppud1q89trfze"
            AND DeleteAt = 0
            ORDER BY 
                CreateAt DESC
            LIMIT 30 OFFSET 0
        ) q3
        WHERE q3.RootId != ''
    ) q1 ON (
        q1.RootId = q2.Id 
     OR q1.RootId = q2.RootId
    )
    WHERE
        ChannelId = "na7oxnzzbfd9pppud1q89trfze"
    AND DeleteAt = 0
    ORDER BY 
        CreateAt)
