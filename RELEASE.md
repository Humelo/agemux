# Release Checklist

1. Run local checks:

   ```sh
   go test ./...
   python3 tests/smoke.py
   python3 tests/public_safety.py
   bash -n scripts/install.sh
   ```

2. Build release artifacts:

   ```sh
   version=0.1.4
   rm -rf dist
   mkdir -p dist

   for os in linux darwin; do
     for arch in amd64 arm64; do
       out="dist/${os}_${arch}"
       mkdir -p "$out"
       GOOS="$os" GOARCH="$arch" go build -o "$out/agemux" ./cmd/agemux
       tar -C "$out" -czf "dist/agemux_${version}_${os}_${arch}.tar.gz" agemux
     done
   done

   for arch in amd64 arm64; do
     out="dist/windows_${arch}"
     mkdir -p "$out"
     GOOS=windows GOARCH="$arch" go build -o "$out/agemux.exe" ./cmd/agemux
     (cd "$out" && zip "../agemux_${version}_windows_${arch}.zip" agemux.exe)
   done
   ```

3. Run review convergence until no P1 findings remain.

4. Tag and create the GitHub release with binary assets:

   ```sh
   git tag v0.1.4
   git push origin main --tags
   gh release create v0.1.4 dist/agemux_0.1.4_* \
     --repo Humelo/agemux \
     --title "Agent Multiplexer v0.1.4" \
     --notes-file RELEASE_NOTES.md
   ```

5. Verify installer dependency behavior after release assets are uploaded:

   ```sh
   tmp="$(mktemp -d)"
   AGEMUX_PREFIX="$tmp" AGEMUX_REF=v0.1.4 scripts/install.sh
   "$tmp/bin/agemux" --help
   "$tmp/bin/agemux" claude-accounts version

   AGEMUX_REPO=<owner>/<repo> AGEMUX_REF=v0.1.4 scripts/install.sh --with-codex-lb
   codex-lb -h
   uv tool list | grep codex-lb
   ```

6. Generate checksums and replace template placeholders:

   ```sh
   sha256sum dist/agemux_0.1.4_*
   curl -L -o dist/agemux-v0.1.4-source.tar.gz https://github.com/Humelo/agemux/archive/refs/tags/v0.1.4.tar.gz
   sha256sum dist/agemux-v0.1.4-source.tar.gz
   ```

   Use the source archive checksum for `packaging/homebrew/agemux.rb.template`.
   Use the Windows release asset checksum for `packaging/scoop/agemux.json.template`.

7. Replace template placeholders, then publish Homebrew tap formula and Scoop bucket manifest:

   - `packaging/homebrew/agemux.rb.template`
   - `packaging/scoop/agemux.json.template`
