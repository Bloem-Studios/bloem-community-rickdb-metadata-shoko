# Publishing to GitHub

The intended repository is:

```text
https://github.com/RickDB/silo-plugin-metadata-shoko
```

Do not push the current working tree until the checks in sections 1–3 pass.

## 1. Finish the repository state

- Review every modified file and confirm no Shoko URL, API key, personal path, log, or generated binary is tracked.
- Replace or remove the inherited TMDB tests in `main_test.go` and `provider/provider_test.go`. They currently target the old TMDB implementation and do not compile against the Shoko provider.
- Run `go mod tidy`, `go test ./...`, `go vet ./...`, and `git diff --check` successfully.
- Confirm that `manifest.json` and the release workflow advertise the same supported platforms.
- Choose the initial public version. The current manifest version is `0.2.4`.

## 2. Resolve the license before publishing

This working directory retains Git history and code derived from Silo Server's AGPL-3.0-or-later TMDB plugin, while the working-tree `LICENSE` currently declares MIT. A copyright holder can license their own new work under MIT, but cannot unilaterally remove obligations attached to inherited AGPL code.

Before publication, choose one defensible route:

1. Use AGPL-3.0-or-later for the derivative repository and preserve the upstream notices; or
2. Document that every inherited implementation has been replaced with independently authored code, obtain any necessary permission, and keep evidence supporting the MIT choice.

When uncertain, get advice from someone qualified to review the actual source and history. Also retain clear attribution to the upstream Silo TMDB repository in the README or a `NOTICE` file.

## 3. Create a clean first commit

Inspect the changes first:

```bash
git status
git diff --check
git diff
```

Then stage and commit only the intended files:

```bash
git add .github .gitignore LICENSE Makefile README.md PUBLISHING.md \
  go.mod go.sum main.go main_test.go manifest.json metadata models provider
git commit -m "feat: add Shoko metadata plugin"
```

If you do not want the TMDB repository's commit history to appear in this project, create a new repository history before the first push. If you preserve that history, keep its license and attribution implications in mind.

## 4. Create the GitHub repository

On GitHub, create a public repository named `silo-plugin-metadata-shoko` under `RickDB`. Do not initialize it with a README, license, or `.gitignore`, because those files already exist locally.

Recommended GitHub **About** settings:

- Description: `Community Silo metadata provider backed by Shoko Server and AniDB.`
- Website: `https://github.com/RickDB/silo-plugin-metadata-shoko/releases`
- Topics: `silo`, `silo-server`, `shoko`, `shoko-server`, `anidb`, `anime`, `metadata`, `golang`
- Enable Issues and Releases.

## 5. Point this checkout at your repository

The current `origin` points at Silo Server's TMDB repository. Preserve that repository as `upstream`, then configure your own repository as `origin`:

```bash
git remote rename origin upstream
git remote add origin https://github.com/RickDB/silo-plugin-metadata-shoko.git
git remote -v
git push -u origin main
```

Using SSH instead:

```bash
git remote add origin git@github.com:RickDB/silo-plugin-metadata-shoko.git
```

Use only one of the two `git remote add origin` commands.

## 6. Configure GitHub Actions

The repository inherits CI and release workflows modeled on Silo's TMDB plugin:

- CI runs tests on pushes to `main` and on pull requests.
- A tag matching `v*` creates checksummed release assets.
- Catalog notification is optional and requires a repository secret named `SILO_PLUGINS_DISPATCH_TOKEN` supplied by the Silo catalog maintainers.

Under **Settings → Actions → General**, allow GitHub Actions to run. The release workflow requires `contents: write`, which is declared in the workflow.

## 7. Publish the first release

Only after CI is green and `manifest.json` contains the same version:

```bash
git tag -a v0.2.4 -m "Shoko metadata plugin v0.2.4"
git push origin v0.2.4
```

Confirm that the GitHub Release contains the binary and `checksums.txt`, then test a fresh installation from those assets. Do not upload a local development binary in place of the workflow-built artifact.

## 8. Match the Silo plugin presentation

The plugin card shown by Silo is driven primarily by the `presentation` block in `manifest.json`. Keep these fields current for every rename or repository move:

- display name, summary, and setup instructions
- source, support, changelog, and publisher URLs
- SPDX license identifier
- supported platforms and plugin capabilities

For catalog inclusion, ask the Silo Server maintainers for their current third-party-plugin submission process and any dispatch token or manifest-index requirements. The release workflow safely skips catalog notification when the secret is absent.
