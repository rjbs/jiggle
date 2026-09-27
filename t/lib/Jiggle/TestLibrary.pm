package Jiggle::TestLibrary;
use v5.36;

use Exporter 'import';
our @EXPORT = qw( library_with built_site );

use Jiggle::Derive;
use Jiggle::Library;
use Jiggle::Photo;
use Jiggle::Site;
use Path::Tiny ();

=head1 NAME

Jiggle::TestLibrary - make throwaway libraries for tests

=head1 DESCRIPTION

C<library_with> builds a library in a temporary directory.  The site builder
only hardlinks renditions, never reads them, so placeholder files stand in
for real images and tests using this don't need libvips.

=cut

# Path::Tiny removes a tempdir when its object is destroyed, so every one is
# kept here until the test ends.  Otherwise a directory could vanish before
# its assertions run, and "never mentioned" checks would pass vacuously.
my @KEEP_TEMPDIRS;

sub library_with (%arg) {
  my $root = Path::Tiny->tempdir;
  push @KEEP_TEMPDIRS, $root;

  $root->child('jiggle.toml')->spew_utf8($arg{config} // '');

  my $library = Jiggle::Library->new({ root => $root });

  for my $spec ($arg{photos}->@*) {
    my $photo = Jiggle::Photo->new({
      original => {
        file => "$spec->{id}.jpg", ext => 'jpg', sha256 => 'f' x 64,
        bytes => 1, width => 4000, height => 3000,
      },
      %$spec,
    });

    $library->add_photo($photo);

    for my $recipe (Jiggle::Derive->recipes) {
      my $file = $library->derived_path($photo->id, $recipe->{name});
      $file->parent->mkpath;
      $file->spew_raw("placeholder");
    }
  }

  for my $album (($arg{albums} // [])->@*) {
    $library->albums_dir->mkpath;
    $library->albums_dir->child("$album->{slug}.toml")->spew_utf8(
      sprintf qq{title = "%s"\nphotos = [%s]\n},
        $album->{title}, join q{, }, map {; qq{"$_"} } $album->{photos}->@*
    );
  }

  return ($library, $root);
}

=head2 built_site

  my ($site, $site_dir, $library) = built_site(%library_args, site => \%site_args);

This makes a library with C<library_with>, then builds its site, passing
C<site> through to L<Jiggle::Site>'s constructor.

=cut

sub built_site (%arg) {
  my $site_arg = delete $arg{site} // {};
  my ($library, $root) = library_with(%arg);
  my $site = Jiggle::Site->new({ library => $library, %$site_arg });
  $site->build;
  return ($site, $root->child('site'), $library);
}

1;
