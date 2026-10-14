# Features & roadmap
Each Zulip topic is bridged as its own room (named `#channel > topic` by default),
and direct messages are bridged as DM/group DM rooms.

* Matrix → Zulip
  * [x] Message content
    * [x] Plain text
    * [x] Formatted messages (converted to Zulip markdown, including mentions and topic links)
    * [x] Media/files (uploaded to Zulip, with captions)
    * [ ] Polls
  * [x] Replies (as Zulip "quote and reply")
  * [x] Message edits (text only)
  * [x] Message redactions
  * [x] Reactions (unicode and Zulip custom emoji by `:name:`)
  * [x] Typing notifications
  * [x] Read receipts
  * [x] Mark as unread
  * [ ] Room metadata changes
* Zulip → Matrix
  * [x] Message content
    * [x] Plain text
    * [x] Formatted messages
    * [x] Media/files
    * [ ] Polls
    * [ ] Todo lists
  * [x] Quote-replies (as native replies when the quoted message is bridged)
  * [x] Message edits
  * [x] Message deletions
  * [x] Reactions
  * [x] Avatars
  * [x] Typing notifications
  * [x] Own read state (read/unread flags)
  * [ ] Other users' read receipts
  * [x] Topic renames and moves (whole topics are re-keyed/merged, partial moves are re-bridged)
  * [x] Channel name/description changes
  * [x] Mute state of channels and topics
  * [x] Initial channel metadata
  * [ ] User metadata changes
  * [x] Initial user metadata
* Misc
  * [x] Automatic portal creation
    * [x] After login (most recently active conversations)
    * [x] When receiving message
    * [x] Catch-up after the event queue expires (backfill + read state reconciliation)
  * [x] Backfill
  * [ ] Private chat creation by inviting Matrix ghost of Zulip user to new room
