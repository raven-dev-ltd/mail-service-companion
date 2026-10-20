# Server-only secrets

This directory is mounted read-only at `/run/secrets`. Put the administrator relay password in `smtp-password`, Firebase Admin SDK key in `firebase-private.json`, and random webhook signing secret in `mail-events-secret` only on your VPS. None are bundled in the image or APK. See [deployment](../docs/DEPLOYMENT.md) for permissions and setup. Files here are ignored by Git; never force-add them.

`google-services.json` is a different file: it is the public Android client configuration downloaded when registering an Android app in Firebase. Upload it in Maker's Firebase configuration picker. It cannot authorize the companion to send notifications, and renaming it to `firebase-private.json` will not convert it into a server key.

To obtain the server file, open the same Firebase project → Project settings → Service accounts → Firebase Admin SDK → Generate new private key. Save the download here as `firebase-private.json`; never upload this private file into Maker or paste it in chat. If key creation is restricted by your organization's policy, ask its Google Cloud administrator rather than disabling that policy. See [Firebase server setup](https://firebase.google.com/docs/admin/setup).
