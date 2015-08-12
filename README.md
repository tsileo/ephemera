# Ephemera

Ephemera is a reverse proxy for ephemeral Docker containers.

When requested, it create a new container (with a TTL), assign a custom id, and proxy request to it.
Once the TTL time is elapsed, it will simply kill and remove the container (and stop proxy request to it).

Used in production to generate private test instance with a 30min TTL for [Blobs](https://blobs.co).

