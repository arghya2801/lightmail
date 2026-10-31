# Lightmail

A small, fast Gmail client for Windows, for reading and sorting mail rather than writing it. It syncs the folders and labels you pick over IMAP, keeps headers in a local SQLite cache, and downloads a message body the first time you open it. You can archive, delete, report spam, star, mark read, move messages between labels (drag and drop works) and manage labels. Changes go to Gmail straight away. There is no compose or reply; use Gmail for that.

Lightmail grew out of [Newsletters](https://github.com/arghya2801/email_newsletter_reader), a stripped-down sibling that only reads newsletter labels.

## Install

Two builds, same app:

- **Installer** (`Lightmail-installer.msi`) installs for your user only, with no admin prompt, into `%LOCALAPPDATA%\Programs\Lightmail`. It adds Start menu and desktop shortcuts and shows up in Settings → Apps for uninstalling. Installing a newer version replaces the old one. Uninstalling keeps your settings and cache.
- **Portable** (`Lightmail-portable.zip`): unzip anywhere, including a USB stick, and run `Lightmail.exe`. The `portable` file next to it keeps everything in a `data` folder beside the exe. Delete `portable` to use `%APPDATA%` instead.

Both need the WebView2 runtime, which ships with Windows 11. On Windows 10, get it from <https://developer.microsoft.com/microsoft-edge/webview2/> if Lightmail won't start.

## Setup

1. Turn on 2-step verification for your Google account, then create an app password at <https://myaccount.google.com/apppasswords>.
2. Make sure IMAP is on in Gmail (Settings → Forwarding and POP/IMAP).
3. Open Lightmail, enter your Gmail address and the app password, and click **Load labels from Gmail**. Every folder is ticked except All Mail and Important, which only repeat messages that live elsewhere. Untick what you don't want, then **Save and sync**.

The first sync fetches headers only, so even a large mailbox lists in a minute or so. After that the Inbox updates within seconds of new mail arriving (IMAP IDLE), and other folders sync on launch and every 15 minutes (configurable).

## Using it

Click a message to open it. Ctrl+click, Shift+click, the checkbox that appears on hover, or `x` select several; the toolbar then acts on all of them. While selecting, clicking a row toggles just that message. Press Esc to clear the selection, or double-click a row to open it. Tab reaches the message list; Up and Down move between rows, Enter opens, Space selects, and Left and Right reach the checkbox and star. A paperclip marks messages with attachments. The last folder reopens on launch if it is still synced. Drag messages onto a label in the sidebar to move them there, or hold Ctrl while dropping to add the label and leave them where they are. Dropping on Trash deletes, on Spam reports spam, on All Mail archives. Every move and delete can be undone from the toast that follows. Errors stay until you dismiss them, and the window title shows the Inbox's unread count.

The headings above the list sort it: Unread, Starred, Sender, Subject and Date. Click one to sort by it alone, and again to reverse it. Shift+click adds it as another level, so Sender then Date groups each sender's mail newest first; Shift+click it again to reverse that level, a third time to remove it. The sort is remembered. While you search or filter by sender, a line above the list shows the scope and how many messages are loaded; click a part to clear it.

Right-click a label, press the Menu key on it, or use its ⋯ button to rename it, delete it, or create one inside it. The + next to Labels creates a top-level one.

Every action has a key on the left half of the keyboard. Gmail's keys work too; they're in the second column. Press `?` in the app for this list, or hover a button to see its key.

| Left hand | Also | Action |
| --- | --- | --- |
| `f` / `d` | `j` / `k` | Next / previous message |
| `Space` / `Shift+Space` | | Page down / up through the message; at the end, Space opens the next one |
| `e` | | Archive |
| `#` | `Delete` | Delete (in Trash or Spam: delete forever) |
| `!` | | Report spam (in Spam: not spam) |
| `z` | | Undo the last move or delete, while its toast is showing |
| `v` | | Move to… |
| `c` | `l` | Add label… |
| `s` | | Star or unstar |
| `t` | `Shift+I` / `Shift+U` | Mark read or unread (toggles) |
| `x` | | Select or deselect the open message |
| `Ctrl+A` / `Esc` | | Select all loaded messages / clear the selection |
| `Ctrl+F` | `/` | Search all mail (subject and sender; body text for messages you've opened). `Esc` clears it, then leaves the box |
| `g 1` … `g 9` | | Open the nth folder in the sidebar (`g 1` is the Inbox) |
| `g f` | `g l` | Go to any folder or label |
| `g s` | | Settings |
| | `g i` | Inbox |
| `Shift+T` | | Switch between Cards and Table |
| `r` | | Sync now |
| `Shift+S` | | Save the message as HTML |
| `Ctrl+Plus` / `Minus` / `0`, `Ctrl`+scroll | | Zoom the message only (remembered; click the % to reset) |
| `Ctrl+Shift+Plus` / `Minus` / `0` | | Zoom the whole app |
| `?` | | Show every key |

In the Move, Label and Go to boxes, type to filter, or press `Tab` / `Shift+Tab` to step through the folders and `Space` to pick one. `Esc` closes the box.

What the actions do in Gmail: archive removes the Inbox label, and the message stays in All Mail. Delete moves it to Trash, which Gmail empties after 30 days. Moving between labels swaps one label for the other. Filing a message from All Mail adds the label instead, because in Gmail a move out of All Mail deletes.

Remote images load like any other part of the message. Because images double as read receipts, Settings can hide them; a message then offers **Load images** and **Always for this sender**. Settings lists allowed senders with a Remove button; save your changes to apply removals. Saved HTML follows the same image permissions, including Load images for the current message. Remote images cannot access private network addresses, and responses must contain a recognized image format. Attachments appear under the message header. Documents, images and media open in their usual app when clicked, marked as downloaded so Office opens them in Protected View; **Save** beside them saves a copy. Anything that could run code (programs, scripts, shortcuts, HTML, archives) can only be saved. Inline images in the message itself always show. Scripts in mail never run.

Settings → Appearance switches between Comfortable (two-line rows) and Compact (one line per message), and sets the app zoom from 80% to 150%. Layout picks Cards (the list beside the message) or Table (sender, subject and date in columns above the message). The two buttons next to the search box switch layouts too, and the choice is remembered.

Drag any pane edge to resize it: the sidebar, the message list (its width, or its height in Table) and the Sender and Date columns. Double-click an edge to put it back. Sizes are remembered; a focused edge also moves with the arrow keys.

## Where things live

Installed: `%APPDATA%\lightmail\`. Portable: `data\` next to the exe. Either holds `config.json` (the app password is encrypted with Windows DPAPI), `cache.db` (SQLite: headers, opened bodies, pending read/star changes) and `img\` (image cache; an image not shown for 30 days is deleted at the next start). Existing plaintext Lightmail passwords are encrypted automatically on launch. DPAPI ties the password to your Windows account and normally your computer: a portable copy on another PC will ask for the app password again. Importing sign-in from the older Newsletters app leaves that app's own settings unchanged. The cache holds nothing Gmail doesn't. Delete `cache.db` to resync from scratch. Set `LIGHTMAIL_DATA` to a folder to run with a separate profile.

## Development

Requires Go 1.26+, Node 20+, and the Wails CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`).

```sh
wails dev            # live reload
wails build          # build/bin/Lightmail.exe
./build.ps1          # installer + portable zip in build/bin (needs WiX 3: unzip wix314-binaries.zip from github.com/wixtoolset/wix3/releases into %LOCALAPPDATA%\wix314)
go test ./...        # parsing, sync, reconcile, every action, IDLE, against an in-memory IMAP server
```

To release, set `productVersion` in `wails.json`, commit, then push a matching tag (`git tag v1.0.0 && git push origin v1.0.0`). GitHub Actions runs the tests, builds the MSI and the portable zip, and attaches them to a draft release for you to review and publish.
