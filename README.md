# mautrix-zulip
A Matrix-Zulip puppeting bridge.

## Documentation
All setup and usage instructions are located on [docs.mau.fi]. Some quick links:

[docs.mau.fi]: https://docs.mau.fi/bridges/go/zulip/index.html

* [Bridge setup](https://docs.mau.fi/bridges/go/setup.html?bridge=zulip)
  (or [with Docker](https://docs.mau.fi/bridges/general/docker-setup.html?bridge=zulip))
* Basic usage: [Authentication](https://docs.mau.fi/bridges/go/zulip/authentication.html)

### This fork
This fork bridges every Zulip topic as its own room (named `#channel > topic`), which keeps Zulip's
per-topic unread state intact. It adds Matrix → Zulip formatting, files, replies, reactions, edits and deletions,
two-way read state, topic rename/move handling, and room creation + backfill right after login.

#### Running with Beeper
```sh
bbctl login
bbctl config --type bridgev2 -o config.yaml sh-zulip
go build -tags goolm -o mautrix-zulip ./cmd/mautrix-zulip
./mautrix-zulip -c config.yaml
```
Then send `login` to the `@sh-zulipbot:beeper.local` bot in Beeper and enter your Zulip server URL, email and API key
(Zulip → Settings → Account & privacy → API key).

### Features & Roadmap
[ROADMAP.md](ROADMAP.md) contains a general overview of what is supported by the bridge.

## Discussion
Matrix room: [#zulip:maunium.net](https://matrix.to/#/#zulip:maunium.net)
