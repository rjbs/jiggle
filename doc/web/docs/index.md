# Jiggle

Jiggle keeps a library of photos and short videos, and publishes it as a
static web site.  It was written to replace Flickr.

The library is served over [JMAP](https://jmap.io/), and everything that
changes it is a JMAP client: the editor, which is a web page, and the
program that adds photos.  Nothing here is mail, so the server defines its
own data model: a capability, `https://semiotic.systems/jmap/jiggle`, with
two object types under it.

- [The JMAP types](types.md): Photo and Album, with the properties of
  each, where their methods differ from the standard ones of
  [RFC 8620](https://www.rfc-editor.org/rfc/rfc8620), and `Photo/import`,
  which makes a photo of an uploaded file.

Jiggle is written in Go, on [Gizmo](https://github.com/rjbs/gizmo), a
library for building JMAP servers.  Its source, and the rest of its
documentation, are [on GitHub](https://github.com/rjbs/jiggle).
