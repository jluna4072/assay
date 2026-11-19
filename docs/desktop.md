# Desktop contract checks

## What

Added contract/rule forms, draft and frozen-version views, a React Flow dependency preview, project selection, run checks, rechecks, saved runs, and attempt history with failure reasons and operational errors.

## Where

`app/frontend/` contains the React/TypeScript view and browser acceptance tests. `app/desktop.go` delegates the Wails bindings to the Go service. `app/main_desktop.go` embeds production assets and opens local SQLite; `app/wails.json` configures development and native builds. CI adds frontend checks and a macOS app build.

## Why

Make the existing acceptance rules and stored decisions usable without adding a second evaluator in the frontend. Users can inspect an earlier failure after a correction and view the published version that governed the run.

## How it's tested

Local frontend type checking and production build, Go race tests/vet/build, Wails binding generation, and browser acceptance tests pass. Browser tests use the actual Go/SQLite backend through a test-only transport; only directory-dialog selection is substituted. They verify fail → correction → pass, retained history after reload, invalid dependency rejection, and immutable published rules.

Native macOS compilation is checked by GitHub Actions on pushes to `main`. Interactive macOS window and directory-dialog use still require a manual check on a Mac.
