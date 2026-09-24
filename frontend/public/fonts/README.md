# Product fonts

Webfonts served by the product frontend. `@font-face` declarations live in
`src/ds/styles/fonts.css`.

| File | Family | Weights | License |
| --- | --- | --- | --- |
| `Inter-400-700.woff2` | Inter (declared as "Inter 92", `size-adjust: 92%`) | 400-700 variable | SIL OFL 1.1 (`Inter-LICENSE.txt`) |
| `RobotoMono-400-500.woff2` | Roboto Mono | 400-500 variable | SIL OFL 1.1 (`RobotoMono-LICENSE.txt`) |

Both are SIL OFL 1.1 and may ship anywhere as long as their license files
travel with them. The display serif, Martina Plantijn, is a retail webfont
licensed from Klim Type Foundry and is not distributable: the hosted
edition bundles and declares it in its own stylesheet, and this edition's
`--font-display` stack renders Georgia.
