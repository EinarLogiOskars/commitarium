# Demo video script

One recording, start to finish, cut into a playlist afterwards. Each part
ends with a cut line ("Next video goes over …") and the next part starts
with a one-line recap, so every part stands alone in the playlist and the
README can link the one it needs.

**How it is used.** First a **test run**: go through the whole script without
recording; every **Check** is a test, so the run doubles as the release test
for 0.11.0. Fix what fails. Then **reset** (below) and go again, recording.

Times are rough targets for the edited parts. Narration is a guide, not lines
to read: say it in your own words.

## Before the test run

Decisions to make first (the proposals are what this script assumes):

- **A separate macOS user for the demo.** Docker Desktop and the keychain are
  per user, so a new user starts empty, and resetting it never touches your
  real projects. Install Docker Desktop there.
- **A fresh server**, deleted afterwards: Ubuntu 24.04, 8 GB of memory so
  part 10 can set up authentik (Hetzner CAX21 or similar). Firewall: 22, 80,
  443 and 7443 open.
- **A domain** for the apps, with access to its DNS: `*.demo.<your domain>`
  pointing at the server (part 9 shows adding it).
- **A Claude and a Codex account** for part 2.
- **A GitHub repository to import** in part 8, small and private, with a
  fine-grained token (Contents: read and write). A second small repository,
  public, for part 6's local import, cloned on the demo user's disk.
- **For part 10**, nothing more: it sets up authentik on the server.

On screen: a clean desktop, one display, notifications off, the window at a
recording size (1440×900 or so), dark or light chosen once for the whole video.

## Part 1 — What Commitarium is, and installing it (5 min)

Opens with: "Commitarium turns a sentence into an app you own, and keeps
changing it as you ask. This series goes through all of it, from installing
to running apps for a whole company. This first part: what it is, and
getting it onto your computer."

1. The README, briefly: the loop at the top, the three promises (no coding,
   yours on your computer, uses the AI you have).
2. Docker Desktop: why it is needed (everything runs in containers on your
   machine), already installed and running.
3. The Releases page: download the `.dmg`. Mention Windows and Linux.
4. Open it, drag to Applications, first launch: Gatekeeper's prompt, Open
   anyway (say why: not code-signed, buildable from source).
5. The first start: checking Docker, pulling images, starting the stack.
   Say it takes a few minutes the first time, then cut the wait.

Check: it opens on Apps with "No apps yet"; Settings > About shows 0.11.0.

Cut: "Next video goes over agents: signing in the AI that does the work."

## Part 2 — Agents (4 min)

Opens with: "Commitarium uses the AI you already pay for. Here is how to
connect it."

1. Agents: add an agent, name it, choose Claude, sign in in the browser.
2. Add a second, Codex. Say why two providers: one builds, the other
   reviews, so no model signs off its own work. Say the first agent of a
   provider takes a couple of minutes to set up, then cut the wait.
3. Mayka: Agents > Mayka, choose the agent and model she runs on, Save.
   Say she writes the plans and every work order, so a strong model pays
   off here.
4. Each agent's usage appears once it has worked (show it in part 3); Mayka
   spreads work by what is left.

Check: both agents show as connected; Mayka's model list fills in by
itself right after the agent is added, and Save works at once.

Cut: "Next video: your first app, from one sentence."

## Part 3 — An app from a sentence (8 min, with the build sped up)

Opens with: "Agents are connected; now let's make something."

1. Apps > New app. Type: "I need an app to track when my plants need water."
2. What happens: Mayka writes a plan, picks a stack, and builds. The tile
   shows progress. Speed this up in the edit, and say how long it really took.
3. The app opens in its own window. Use it: add a plant, mark it watered.
4. Ask for a change from inside the app: "Add a thirst bar to each plant."
   Watch it change.

Check: the app builds and opens without help; the change lands and the app
reloads with it; the agents now show their usage.

Cut: "Next video opens the hood: Developer mode."

## Part 4 — Developer mode (6 min)

Opens with: "Without Developer mode, Commitarium is just your apps. Turn it
on, and you see the workshop behind them."

1. Settings > General > Developer mode.
2. The sidebar: projects and their chats. The plant app is a project.
3. The chat with Mayka in the centre; the cards beside it: plan, work
   orders, agents at work, preview, sync.
4. The two modes: Assists me (you approve each step) and Builds everything
   for me (Mayka owns it). Apps start in Build for me. Project settings: merge
   policy, autonomy.
5. A work order from part 3: its plan, the reviewer's objections, the code
   review, the merge.

Check: every card opens its page and goes back to the chat.

Cut: "Next video: planning a bigger change with Mayka."

## Part 5 — Planning with Mayka (7 min)

Opens with: "For something bigger than a tweak, think it through first."

1. In the plant app's chat, switch on plan mode (`/plan`): "Let me share
   plants with my partner, so we both see what needs water."
2. Mayka asks what she needs, then writes a plan in commit-sized slices.
3. Build it: autopilot starts the slices; two run side by side.
4. Switch the project to Assists me for a moment: a merge waits for you;
   approve it.

Check: the plan is written, the slices run and merge, the preview shows
sharing.

Cut: "Next video: bringing in a project you already have."

## Part 6 — Your own code (5 min)

Opens with: "Commitarium isn't only for new apps. Here is an existing
project."

1. Projects > Import project: the public repository's folder.
2. Mayka's first look: what it is, the stack, a numbered list of what
   could come next, how the project is set up, and the Build for me card.
3. Ask her to do one item from the list.
4. Sync: the change comes back to the folder only when you ask.

Check: the first look arrives on its own; the item becomes a work order and
merges; the sync writes it to the folder.

Cut: "Next video: a server of your own, so the work carries on when your
computer is off."

## Part 7 — A server of your own (6 min)

Opens with: "Everything so far ran on this computer. A server keeps working
when the laptop is closed."

1. The fresh server: SSH in, run the install line from the server guide.
   It offers to install Docker, starts everything, prints a
   `commitarium://` line.
2. In the app: the menu under Commitarium's name > Connect a server; paste
   the line. Say how it pairs: a one-time code, then a key that stays in
   the keychain.
3. Switch between This computer and the server: each keeps working.
4. Settings > Server: name it, devices, updates.
5. Sign in agents on the server (Agents, as in part 2).

Check: pairing works the first time; switching back and forth is quick; the
server is up to date.

Cut: "Next video: working on the server with a GitHub repository."

## Part 8 — Projects on a server, and GitHub (7 min)

Opens with: "On a server, your code lives in its own git server. Here is how
it stays in step with GitHub."

1. Import project by URL: the private repository and its token.
2. The Linked repository card. Make a change on GitHub and one in
   Commitarium, then Pull: both sides changed, so Mayka merges them in a
   work order.
3. Push: Commitarium's main goes to a branch you name on GitHub, never its
   main; open the pull request and merge it there.
4. The audit viewer: the server's git history, from this device.

Check: the import links the repository; the pull merges both sides; the push
creates the branch and the pull request link works.

Cut: "Next video: putting an app online."

## Part 9 — Putting an app online (7 min)

Opens with: "An app on a server can be on the internet, at its own
address."

1. Settings > Server > Domain: enter the domain; it shows the DNS record to
   add (`*.demo.<domain>`); add it at the DNS provider; Use this domain.
2. The project's Deployment card: Deploy. Say the deployment is separate from
   the preview.
3. Give it an address, `plants`, and choose who can open it: only with a
   password. Open it in a browser on the phone.
4. Change the address: the warning that the old one stops working.
5. Deploy an earlier version, then roll forward.

Check: the certificate is issued; the password prompt works; the warning
shows; rollback works.

Cut: "Next video: letting a team sign in with their company accounts."

## Part 10 — Company sign-in (8 min)

Opens with: "A password is fine for a few people. For a company, people sign
in with their work accounts, and each app chooses who may open it."

1. On the plant app's Deployment page: Who can open it > Company sign-in
   (set up first) > Set it up with Mayka.
2. Mayka asks: your own provider, or do you want one? Say you want one, and
   that you are a team of 40. She recommends authentik and says what it
   costs (2 to 3 GB of memory, a server of 8 GB or more). For contrast, say
   what she'd suggest for three people (Pocket ID, which she'd help set up).
3. Settings > Server > Company sign-in: the memory note, the server's own
   memory, Set up authentik. A few minutes.
4. authentik's admin page: show password, sign in, add a person and a group
   `gardeners`.
5. The app: Company sign-in, Choose groups: `gardeners`. Open it in a
   private window: sign in as the new person; it opens. Remove them from the
   group: "this app is for other groups".
6. Ask Mayka to greet people by name: she uses who signed in.
7. Mention connecting an existing provider instead (Microsoft, Google), with
   her steps for it.

Check: authentik installs and connects; sign-in, the group check and the
greeting all work.

Cut: "Next video: looking after a server."

## Part 11 — Looking after a server (5 min)

Opens with: "A server looks after itself, mostly. Here is what it does, and
what you can do."

1. Settings > Backup: the nightly backups; Back up now; the app says
   "backing up" while the server is busy.
2. Restore one: projects, apps, authentik's people all come back.
3. Disk: what it clears by itself.
4. Updates: the server's update in Settings > Server.

Check: the backup and restore keep everything, authentik included; the
deployed app comes back by itself.

Cut: "Next video: sharing apps in the marketplace." (or the outro, if part
12 is left out)

## Part 12 — The marketplace (3 min, optional)

1. Release the plant app; install someone else's app as your own copy.

Check: release and install both work.

## Outro (1 min)

What it is in a sentence, where to download, where the series is, how to
report problems.

## Reset between the test run and the recording

On the computer, as the demo user:

1. Quit Commitarium.
2. In Docker Desktop: Troubleshoot > Clean / Purge data (removes all of the
   demo user's containers, images and volumes).
3. Delete `~/Library/Application Support/com.einarlogi.commitarium`.
4. In Keychain Access, delete the `Commitarium` item (`device-key`).
5. Delete Commitarium from Applications, and the downloaded `.dmg`.

On the server: rebuild it from the provider's console (the fastest way back
to a fresh Ubuntu), or run the uninstall line, then
`rm -rf /var/lib/commitarium` and remove its Docker volumes.

On GitHub: delete the branch part 8 pushed, revert the change made there,
and make a new token if the old one appeared on screen.

At the DNS provider: keep the record; it points at the server's address,
which a rebuild keeps.
