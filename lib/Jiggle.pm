package Jiggle;
use v5.36;

1;

=head1 NAME

Jiggle - a static photo site built from plain files

=head1 OVERVIEW

A jiggle library is a directory holding a F<jiggle.toml> configuration file
and three trees: F<originals> (write-once original files), F<meta> (one TOML
file per photo, plus albums), and F<derived> (generated renditions, a cache).
The C<jiggle> command ingests new photos into a library and builds a static
site from it.

See F<PLAN.md> in the distribution for the design, and F<STORAGE.md> for the
library's layout.

=head1 THE NAME

jiggle is named for jGal, rjbs's first static gallery generator, written in
2003 because the gallery tools of the day wanted mod_perl or PHP, or made
thumbnails on the fly.  jGal ran ImageMagick once and wrote plain XHTML with
classes for CSS to style.  jiggle runs libvips once and writes plain HTML
for CSS to style.  jGal's to-do list included "extract and display
(optionally) EXIF data"; jiggle reads every photo's EXIF, and then carefully
strips it from everything it publishes.

=cut
