# Vendored OKF example bundles

`okf-crypto-bitcoin/` and `okf-ga4/` are unmodified OKF bundles (minus each
bundle's `viz.html`, which is a CDN-dependent viewer not needed here) vendored
from Google Cloud's knowledge-catalog reference repository:

https://github.com/GoogleCloudPlatform/knowledge-catalog/tree/main/okf/bundles

Those bundles are licensed under the Apache License 2.0, a copy of which is at
https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/LICENSE.md.
Copyright the OKF / knowledge-catalog authors. The `.waqwaq/config.json` in each
vendored directory was added to enable OKF mode when serving; the markdown
content is unchanged.

The `crypto_bitcoin` and `ga4` bundles describe Google BigQuery public datasets.
The `stackoverflow` bundle from the same repository was deliberately not vendored:
its content derives from the Stack Exchange data dump under CC-BY-SA 4.0, whose
share-alike terms do not sit cleanly under this repository's MIT license.

`okf-demo/` and `waqwaq-myth/` are original example bundles authored for this
project and carry the repository's MIT license.
