rss-expert
==========

A social reader for the open web. It reads feeds, publishes feeds, and threads
conversations over plain RSS. One static binary, one SQLite file, one container.


Build
-----

    go build ./cmd/rss-expert
    go test ./...

Needs Go 1.25 or later. No cgo, no Node, no build step for the assets.


Run
---

    docker compose up -d

Or run the binary directly:

    RSS_EXPERT_DOMAIN=example.org ./rss-expert serve

Only RSS_EXPERT_DOMAIN is required. The first owner account is made on first
boot from RSS_EXPERT_ADMIN_EMAIL and RSS_EXPERT_ADMIN_PASSWORD; clear the
password and restart once you have signed in. See .env.example for the rest.


Commands
--------

    rss-expert serve      run it
    rss-expert migrate    apply the schema, then stop
    rss-expert doctor     check the environment and the data
    rss-expert backup     write a snapshot
    rss-expert restore    restore a snapshot
    rss-expert version


Author
------

Pablo Murad
https://github.com/runawaydevil


License
-------

AGPL-3.0. See LICENSE.
