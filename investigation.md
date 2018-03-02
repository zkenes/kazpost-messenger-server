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
                ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
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
        ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
    AND DeleteAt = 0
    ORDER BY 
        CreateAt

Observe that the innermost query matches the `getRootPosts` query, except for fetching only the
root post ids in question. As this query occurs outside of a transaction, there is a possibility 
for the duplicate `getRootPosts` query to find a different set of root posts and potentially return 
incorrect results on a busy channel. This appears to be partly by design, as the `getRootPosts`
and `getParentsPosts` are run concurrently.

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
threads is discarded. (The order from `getRootPosts` is explicitly preserved.) Indeed, ordering
randomly (`ORDER BY RAND()`) and then removing the `ORDER BY` altogether has no impact on the
output from the corresponding endpoint.

However, this is at best a minor improvement, given the result size of a typical query. It will
remove a `Using temporary; Using filesort` in the query plan (on MySQL).

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

This almost exactly duplicates the handling of `getParentsPosts` above, except for recording
the order in which the root posts are returned. For completeness, the query run by `getRootPosts`
is:

    SELECT 
        * 
    FROM 
        Posts 
    WHERE 
        ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e" 
    AND DeleteAt = 0 
    ORDER BY 
        CreateAt DESC 
    LIMIT 30 OFFSET 0

Merging these two queries (removing the superfluous `ORDER BY` as above) and adding a bit to 
identify the centre channel posts and thus extract the required order looks like this:

    SELECT
        post.*,
        post.Id = windowPost.Id AS InWindow
    FROM (
        SELECT
            Id,
            RootId
        FROM
            Posts
        WHERE
            ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
        AND DeleteAt = 0
        ORDER BY 
            CreateAt DESC
        LIMIT 30 OFFSET 0
    ) windowPost
    JOIN Posts post ON (
        -- The posts in the window
        post.Id = windowPost.Id
        -- The root post of any replies in the window
     OR post.Id = windowPost.RootId
        -- The reply posts to all threads intersecting with the window.
     OR (post.RootId != '' AND post.RootId = windowPost.RootId)
        -- Fetch replies to the posts in the window.
     OR post.RootId = windowPost.Id
    )
    WHERE
        post.DeleteAt = 0
    ORDER BY 
        CreateAt DESC

The computed `InWindow` gives enough context to rebuild the `Order` array in the `PostList`:

    for _, p := range posts {
            list.AddPost(p)
            if post.InWindow {
                list.AddOrder(p.Id)
            }
    }

Note that the above query deviates slightly from the original in explicitly querying for replies to
posts in the window:

        -- Fetch replies to the posts in the window.
     OR post.RootId = windowPost.Id

In the webapp, such replies are normally already loaded or within the window themselves. And
permalinks use a different query (`getPostsAround`). But a client of the REST API won't get the
full thread included in the response like they would at other offsets. Patching the original
`getParentsPosts` query to fix this might look like, the following:

    SELECT
        q2.*
    FROM
        Posts q2
    INNER JOIN (
        SELECT DISTINCT
            q3.RootId
        FROM (
            SELECT
                COALESCE(RootId, Id) AS RootId
            FROM
                Posts
            WHERE
                ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
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
        ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
    AND DeleteAt = 0
    ORDER BY 
        CreateAt

noting the addition of the following within the inner query:

    COALESCE(RootId, Id) AS RootId

All that being aside, does merging the query actually improve performance given that this no
longer parallelizes the original two calls to `getRootPosts` and `getParentsPosts`? Ten runs
of each of the queries on a local MySQL instance with 240K posts yielded the following minimums:

| getRootPosts | getParentsPosts | getParentsPosts (patched) | Merged |
| ------------ | --------------- | ------------------------- | ------ |
| 0.00s        | 0.19s           | 0.19s                     | 2.74s  |

Hmm, something is amiss. Running `EXPLAIN FORMAT=json` on the merged query shows a 
`Block Nested Loop` on the `Posts` table. Looks like MySQL doesn't even bother trying to use an
index. Adding in the `ChannelId` constraint to the outer `SELECT`:

    WHERE
        ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
    AND DeleteAt = 0

causes MySQL to choose the `idx_posts_channel_id_delete_at_create_at` index and results in:

| getRootPosts | getParentsPosts | getParentsPosts (patched) | Merged (with channel contraint) |
| ------------ | --------------- | ------------------------- | ------------------------------- |
| 0.00s        | 0.19s           | 0.19s                     | 0.35s                           |

Much better, but also twice as slow as running `getRootPosts` and the patched `getParentsPosts`
sequentially, much less concurrently. Does it get any better with a larger `Posts` table, say 960K?

Nope:

| getRootPosts | getParentsPosts | getParentsPosts (patched) | Merged (with channel contraint) |
| ------------ | --------------- | ------------------------- | ------------------------------- |
| 0.00s        | 2.83s           | 2.81s                     | 3.68s                           |

Stepping back, the goal here was to select the `Id` and `RootId` of the window posts (a small
number of posts), and then fetch the related posts from the `Posts` table. That could be lightening
quick, but MySQL insists on doing a table scan. What if we drop the `ChannelId` constraint and
rewrite the query in a MySQL-specific way that forces that behaviour?

    SELECT
        post.*,
        post.Id = windowPost.Id AS InWindow
    FROM
        Posts post FORCE INDEX (PRIMARY, idx_posts_root_id)
    JOIN (
        SELECT
            Id,
            CASE RootId
                WHEN "" THEN NULL
                ELSE RootId
            END AS RootId
        FROM
            Posts
        WHERE
            ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
        AND DeleteAt = 0
        ORDER BY 
            CreateAt DESC
        LIMIT 30 OFFSET 0
    ) windowPost ON (
        -- The posts in the window
        post.Id = windowPost.Id
        -- The root post of any replies in the window
     OR post.Id = windowPost.RootId
        -- The reply posts to all threads intersecting with the window.
     OR post.RootId = windowPost.RootId
        -- Fetch replies to the posts in the window.
     OR post.RootId = windowPost.Id
    ) 
    WHERE
        DeleteAt = 0
    ORDER BY 
        CreateAt DESC

Wow! And this is still on the larger table size:

| getRootPosts | getParentsPosts | getParentsPosts (patched) | Merged (hints) |
| ------------ | --------------- | ------------------------- | -------------- |
| 0.00s        | 2.83s           | 2.81s                     | 0.02s          |

Using an `EXPLAIN`, MySQL stopped doing a table scan in favour of a 

    "range_checked_for_each_record": "index map: 0x21"

Which effectively means it's using the `PRIMARY` and `idx_posts_root_id` indices, and doing
an index merge after selecting against both. This is basically what I had hoped MySQL would have
done in the first place.

Unfortunately, query hints are database-specific, and it's unclear if Aurora supports index merges
at all. Can we rewrite the query in a way that gets the desired behaviour naturally? (Note that
MySQL has lots of limitations that make the following needlessly verbose. Oh for support for
CTEs!)

    SELECT
        *,
        Id IN (SELECT * FROM (
            SELECT
                Id
            FROM
                Posts
            WHERE
                ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
            AND DeleteAt = 0
            ORDER BY 
                CreateAt DESC
            LIMIT 30 OFFSET 0
        ) x) AS InWindow
    FROM
        Posts
    WHERE
        Id IN (SELECT * FROM (
            -- The posts in the window
            (SELECT * FROM (
                SELECT
                    Id
                FROM
                    Posts
                WHERE
                    ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
                AND DeleteAt = 0
                ORDER BY 
                    CreateAt DESC
                LIMIT 30 OFFSET 0
            ) x )

            UNION

            -- The root post of any replies in the window
            (SELECT * FROM (
                SELECT
                    CASE RootId
                        WHEN '' THEN NULL
                        ELSE RootId
                    END
                FROM
                    Posts
                WHERE
                    ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
                AND DeleteAt = 0
                ORDER BY 
                    CreateAt DESC
                LIMIT 30 OFFSET 0
            ) x )

            UNION

            -- The reply posts to all threads intersecting with the window.
            (
                SELECT
                    Id
                FROM
                    Posts
                WHERE RootId IN (SELECT * FROM (
                    SELECT
                        CASE RootId
                            WHEN '' THEN NULL
                            ELSE RootId
                        END
                    FROM
                        Posts
                    WHERE
                        ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
                    AND DeleteAt = 0
                    ORDER BY 
                        CreateAt DESC
                    LIMIT 30 OFFSET 0
                ) x )
            )

            UNION

            -- Fetch replies to the posts in the window.
            (
                SELECT
                    Id
                FROM
                    Posts
                WHERE RootId IN (SELECT * FROM (
                    SELECT
                        Id
                    FROM
                        Posts
                    WHERE
                        ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
                    AND DeleteAt = 0
                    ORDER BY 
                        CreateAt DESC
                    LIMIT 30 OFFSET 0
                ) x )
            )
        ) x )
    AND 
        DeleteAt = 0;


| getRootPosts | getParentsPosts | getParentsPosts (patched) | Merged (inlined) |
| ------------ | --------------- | ------------------------- | ---------------- |
| 0.00s        | 2.83s           | 2.81s                     | 0.02s            |





---------

`idx_posts_channel_id_delete_at_create_at` index to constrain to the channel first and then  instead of, say, 


4u1zsjt8hinkuxzy76tdgygf5e

getRootPosts:

    SELECT 
        * 
    FROM 
        Posts 
    WHERE 
        ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e" 
    AND DeleteAt = 0 
    ORDER BY 
        CreateAt DESC 
    LIMIT 30 OFFSET 0

getParentsPosts:

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
                ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
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
        ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
    AND DeleteAt = 0
    ORDER BY 
        CreateAt

getParentsPosts (patched):

    SELECT
        q2.*
    FROM
        Posts q2
    INNER JOIN (
        SELECT DISTINCT
            q3.RootId
        FROM (
            SELECT
                COALESCE(RootId, Id) AS RootId
            FROM
                Posts
            WHERE
                ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
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
        ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
    AND DeleteAt = 0
    ORDER BY 
        CreateAt

merged (FAST!)

    SELECT
        post.*,
        post.Id = windowPost.Id AS InWindow
    FROM
        Posts post FORCE INDEX (PRIMARY, idx_posts_root_id)
    JOIN (
        SELECT
            Id,
            CASE RootId
                WHEN "" THEN NULL
                ELSE RootId
            END AS RootId
        FROM
            Posts
        WHERE
            ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
        AND DeleteAt = 0
        ORDER BY 
            CreateAt DESC
        LIMIT 30 OFFSET 0
    ) windowPost ON (
        -- The posts in the window
        post.Id = windowPost.Id
        -- The root post of any replies in the window
     OR post.Id = windowPost.RootId
        -- The reply posts to all threads intersecting with the window.
     OR post.RootId = windowPost.RootId
        -- Fetch replies to the posts in the window.
     OR post.RootId = windowPost.Id
    ) 
    WHERE
        DeleteAt = 0
    AND ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
    ORDER BY 
        CreateAt DESC

merged (FAST, 2!)

    SELECT
        post.*,
        post.Id = windowPost.Id AS InWindow
    FROM
        Posts post IGNORE INDEX (idx_posts_channel_id_delete_at_create_at)
    JOIN (
        SELECT
            Id,
            CASE RootId
                WHEN "" THEN NULL
                ELSE RootId
            END AS RootId
        FROM
            Posts
        WHERE
            ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
        AND DeleteAt = 0
        ORDER BY 
            CreateAt DESC
        LIMIT 30 OFFSET 0
    ) windowPost ON (
        -- The posts in the window
        post.Id = windowPost.Id
        -- The root post of any replies in the window
     OR post.Id = windowPost.RootId

        -- The reply posts to all threads intersecting with the window.
     OR post.RootId = windowPost.RootId
        -- Fetch replies to the posts in the window.
     OR post.RootId = windowPost.Id
    ) 
    WHERE
        DeleteAt = 0
    AND ChannelId = "4u1zsjt8hinkuxzy76tdgygf5e"
    ORDER BY 
        CreateAt DESC

