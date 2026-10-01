package Jiggle;
use v5.36;

use File::ShareDir ();
use Path::Tiny ();

# In a checkout, share/ is beside lib/; installed, it's wherever
# File::ShareDir put it.  The checkout wins, so a working copy uses its own
# templates even when another jiggle is installed.  -- claude, 2026-09-30
sub share_dir ($class) {
  my $here = Path::Tiny::path(__FILE__)->absolute->parent(2)->child('share');
  return $here if -d $here->child('templates');
  return Path::Tiny::path(File::ShareDir::dist_dir('Jiggle'));
}

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

=head1 METHODS

=head2 share_dir

This returns the directory of jiggle's templates, stylesheets, scripts, and
editor: F<share> in a checkout, or the installed distribution's share
directory.

=head1 THE NAME

jiggle is named for jGal, rjbs's first static gallery generator, written in
2003 because the gallery tools of the day wanted mod_perl or PHP, or made
thumbnails on the fly.  jGal ran ImageMagick once and wrote plain XHTML with
classes for CSS to style.  jiggle runs libvips once and writes plain HTML
for CSS to style.  jGal's to-do list included "extract and display
(optionally) EXIF data"; jiggle reads every photo's EXIF, and then carefully
strips it from everything it publishes.

=cut
