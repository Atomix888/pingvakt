# PingVakt for Android

The app runs the PingVakt agent on the phone (the Go agent in `mobile` mode, packaged as
`libpvagent.so`) and shows its interface in a WebView. Kotlin feeds it Wi-Fi data, network
information and scan results; the agent measures and reports to the server like any other agent.

Build (CI does this in the `android` job):

```bash
# agent binaries for the three ABIs
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.Version=$VERSION" -o android/app/src/main/jniLibs/arm64-v8a/libpvagent.so ./agent
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags "-s -w -X main.Version=$VERSION" -o android/app/src/main/jniLibs/armeabi-v7a/libpvagent.so ./agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.Version=$VERSION" -o android/app/src/main/jniLibs/x86_64/libpvagent.so ./agent
gradle -p android assembleRelease
```

The APK is signed with `pingvakt.keystore` (for sideloading) unless the CI secrets
`ANDROID_KEYSTORE` (base64), `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS` and
`ANDROID_KEY_PASSWORD` are set. Keep using the same key, otherwise Android refuses to update
an installed app.
