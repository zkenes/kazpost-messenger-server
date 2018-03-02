# getParentsPosts query investigation

## Terminology

For the purpose of this discussion, I've defined the following terms to hopefully match their use
within the code base.

* *post*: a message posted to a channel
* *root post*: a post not made in reply to any other post
* *reply post*: a post made in reply to another post
* *thread*: a set of posts including a root post and any reply posts
* *window*: a subset of a channel's posts, queried by offset and limit in this document

## Executive Summary

There is substantial room for improvement, as well as fixing a bug:
* the query includes a superfluous ORDER BY (minor savings)
* the query doesn't fetch all threads in all cases (minor expense)
* the default query plan is horribly inefficient, and given the constraints of the problem, we
can improve performance by three orders of magnitude in the average case, and one order of
magnitude in the worst case (huge savings!)

## Overview

`getParentsPosts` is invoked as part of `GetPosts`, which itself accepts a `channelId`, `offset` 
and `limit` defining a window for the query. For the webapp, the typical window is the 30 most
recent posts for the given channel.

`GetPosts` first invokes `getRootPosts`. This method is somewhat of a misnomer, since reply posts
are visually interleaved with root posts, and this method fetches a window potentially containing 
both. This corresponding query is as follows:

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

`GetPosts` then invokes `getParentsPosts`. This method, itself awkwardly named, fetches all threads
having posts in the same window. For example, if a root past was made last year, and received
100 reply posts, the most recent of which occurred in the current window, that root post and all 
100 reply posts are loaded by `getParentsPosts`, along with any other such threads. The effort
here overlaps with `getRootPosts` for any thread posts in the window.

This results of this second query are used client side to render a count of replies when hovering
over a root post, as well as allowing the RHS for a given thread to be opened without any
additional network call.

The query in question is:

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
root post ids in question. As `getParentsPosts` and `getRootPosts` queries are run concurrently
and outside of a repeatable read transaction, there is a possibility that they arrive at a 
different definition of the window on a busy channel. This in turn might lead to "heisenbugs" where
the reply count or RHS are not correct.

## Query Improvements

As a baseline, the unmodified queries that make up a call to `GetPosts` on a database with ten 
channels and about 940K posts averages as follows:

| getRootPosts | getParentsPosts |
| ------------ | --------------- |
| 0.00s        | 3.108s          |

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
threads is discarded. (Note that the order from `getRootPosts` is explicitly preserved in a
separate array.) Indeed, ordering randomly (`ORDER BY RAND()`) or removing the `ORDER BY` 
altogether has no impact on the output from the corresponding endpoint.

This is at best a minor improvement, given the result size of a typical query, but it will remove
a `Using temporary; Using filesort` in the query plan (on MySQL) without any downside. An average
of ten runs shows:

| getParentsPosts | + removing ORDER BY |
| --------------- | ------------------- |
| 3.108s          | 3.067               |

### Fixing missing replies

The existing JOIN conditions on the query could be commented as follows:

       -- The root post of any replies in the window
       q1.RootId = q2.Id 
       -- The reply posts to all threads intersecting with the window.
    OR q1.RootId = q2.RootId

however it is missing a case:

        -- The reply posts to the window posts, themselves not in the window
    OR q1.Id = q2.RootId

Specifically, this handles the case where a reply to the post in the window isn't in the window
itself. In practice, given that the webapp starts at offset 0 and queries backwards on demand, any 
replies to posts in the window will have already been in a loaded window themselves. Permalinks 
at first seems like a case where this behaviour would manifest, but they employs a different
query (`getPostsAround`).

So the only case where this might matter is a client of the REST API building a resultset and
starting from a non-zero offset. The patched version has a minimal negative impact on performance:

| getParentsPosts | + removing ORDER BY | + patching case |
| --------------- | ------------------- | --------------- |
| 3.108s          | 3.067               | 3.085           |

### Improving the query plan

The query plan with the changes above shows MySQL using an index merge on `idx_posts_channel_id`
and `idx_posts_delete_at` to fetch the inner root posts, a filter on the `Posts` channel using
`idx_posts_channel_id_delete_at_create_at`, and a `Block Nested Loop` to join the results together.

This is unfortunate, since the inner query is always a constrained set of post ids. It might be
faster just to use the `PRIMARY` and `idx_posts_root_id` indices. To start, we can force that
using proprietary hints:

    FROM
        Posts post FORCE INDEX (PRIMARY, idx_posts_root_id)

The results are startling, though not surprising given the nuances of query planners:

| getParentsPosts | + removing ORDER BY | + patching case | + hinting |
| --------------- | ------------------- | --------------- | --------- |
| 3.108s          | 3.067               | 3.085           | 0.005     |

Does this still work with a larger limit, say the limit of `60` used by load tests?

| getParentsPosts + removing ORDER BY + patching case (60) | + hinting |
| -------------------------------------------------------- | --------- |
| 3.201                                                    | 0.002     |

Yes! (The numbers being better than the case of `30` suggest that the query is not bound by the 
usual performance constraints.) What about `1000`, the maximum limit allowed? Not as much:

| getParentsPosts + removing ORDER BY + patching case (1000) | + hinting |
| ---------------------------------------------------------- | --------- |
| 6.857                                                      | 7.984     |

Why not? The query plan for higher limits is very different, suggesting that MySQL is once again
taking a different path. Before examining further, let's rewrite the query in a way that preserves
these semantics but avoids proprietary hints at the expense of some MySQL-imposed verbosity:

    SELECT
        *
    FROM
        Posts
    WHERE
        Id IN (SELECT * FROM (
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
                    ChannelId = :ChannelId
                AND DeleteAt = 0
                ORDER BY 
                    CreateAt DESC
                LIMIT :Limit OFFSET :Offset
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
                        ChannelId = :ChannelId
                    AND DeleteAt = 0
                    ORDER BY 
                        CreateAt DESC
                    LIMIT :Limit OFFSET :Offset
                ) x )
            )

            UNION

            -- The reply posts to the window posts, themselves not in the window
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
                        ChannelId = :ChannelId
                    AND DeleteAt = 0
                    ORDER BY 
                        CreateAt DESC
                    LIMIT :Limit OFFSET :Offset
                ) x )
            )
        ) x )
    AND 
        DeleteAt = 0;

The performance is similar to the hinting case for limits of `30` and `60`:

| getParentsPosts + removing ORDER BY + patching case (30) | + hinting | rewritten |
| --------------- | ------------------- | ---------------- | --------- | --------- |
| 3.108s          | 3.067               | 3.085            | 0.005     | 0.007     |

| getParentsPosts + removing ORDER BY + patching case (60) | + hinting | rewritten |
| -------------------------------------------------------- | --------- | --------- |
| 3.201                                                    | 0.002     | 0.006     |

and blows everything else out of the water:

| getParentsPosts + removing ORDER BY + patching case (1000) | + hinting | rewritten |
| ---------------------------------------------------------- | --------- | --------- |
| 6.857                                                      | 7.984     | 0.27      |

Hurrah for foiling the query planner!

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
the order in which the root posts are returned. Though the performance issues are resolved by
the improvements above, we could avoid sometimes fetching duplicate data by truly running both
queries at once:

    SELECT
        *,
        Id IN (SELECT * FROM (
            SELECT
                Id
            FROM
                Posts
            WHERE
                ChannelId = :ChannelId
            AND DeleteAt = 0
            ORDER BY 
                CreateAt DESC
            LIMIT :Limit OFFSET :Offset
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
                    ChannelId = :ChannelId
                AND DeleteAt = 0
                ORDER BY 
                    CreateAt DESC
                LIMIT :Limit OFFSET :Offset
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
                    ChannelId = :ChannelId
                AND DeleteAt = 0
                ORDER BY 
                    CreateAt DESC
                LIMIT :Limit OFFSET :Offset
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
                        ChannelId = :ChannelId
                    AND DeleteAt = 0
                    ORDER BY 
                        CreateAt DESC
                    LIMIT :Limit OFFSET :Offset
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
                        ChannelId = :ChannelId
                    AND DeleteAt = 0
                    ORDER BY 
                        CreateAt DESC
                    LIMIT :Limit OFFSET :Offset
                ) x )
            )
        ) x )
    AND 
        DeleteAt = 0;

The computed `InWindow` gives enough context to rebuild the `Order` array in the `PostList`:

    for _, p := range posts {
            list.AddPost(p)
            if post.InWindow {
                list.AddOrder(p.Id)
            }
    }

Running this combined query for limits of `30`, `60` and `1000` show the query to be about as fast
as running `getRootPosts` and `getParentsPosts` concurrently:

| getRootPosts (30) | getParentsPosted rewritten (30) | merged getPosts (30) |
|-------------------|---------------------------------|----------------------|
| 0.00s             | 0.007                           | 0.007                |

| getRootPosts (60) | getParentsPosted rewritten (60) | merged getPosts (60) |
|-------------------|---------------------------------|----------------------|
| 0.00s             | 0.006                           | 0.008                |

| getRootPosts (1000) | getParentsPosted rewritten (1000) | merged getPosts (1000) |
|---------------------|-----------------------------------|------------------------|
| 0.009s              | 0.27                              | 0.269                  |

But might yield some minor network savings for having to transmit the data to the client.

If MySQL were self-consistent with respect to subqueries, this would also eliminate the potential
for a race condition, but I suspect (but have not verified) that it is not. In more recent versions
of MySQL (and Postgres), we could leverage CTEs to achieve this and simplify the query.

## Follow-up


