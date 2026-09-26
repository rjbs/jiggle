use v5.36;

use Test::More;

use lib 'lib';

use Jiggle::Derive;
use Jiggle::Library;
use Jiggle::Photo;
use Jiggle::Site;
use Path::Tiny ();

# Build a library in a temporary directory.  The site builder only hardlinks
# renditions, never reads them, so placeholder files stand in for real images
# and these tests don't need libvips.
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

sub built_site (%arg) {
  my ($library, $root) = library_with(%arg);
  my $site = Jiggle::Site->new({ library => $library });
  $site->build;
  return ($site, $root->child('site'), $library);
}

sub site_files ($dir) {
  my @files;
  my $iter = $dir->iterator({ recurse => 1 });
  while (my $f = $iter->()) {
    push @files, $f->relative($dir)->stringify unless $f->is_dir;
  }
  return sort @files;
}

sub never_mentioned_ok ($desc, $dir, $needle) {
  my @hits = grep {; $dir->child($_)->slurp_raw =~ /\Q$needle/ } site_files($dir);
  is_deeply(\@hits, [], "$desc: no published file mentions $needle");
  ok(! -e $dir->child("p/$needle"), "$desc: no directory for $needle");
}

sub location_published_is ($desc, $site, $photo, $want) {
  is_deeply(scalar $site->public_location($photo), $want, "location: $desc");
}

subtest 'private photos are never published' => sub {
  my ($site, $dir) = built_site(
    photos => [
      { id => 'pub00001', taken => '2026-07-17T10:00:00+02:00', tags => [ 'vienna' ],
        location => { lat => 48.2, lon => 16.37 } },
      { id => 'priv0001', taken => '2026-07-18T10:00:00+02:00', tags => [ 'vienna', 'secret' ],
        location => { lat => 48.21, lon => 16.38 }, visibility => 'private' },
    ],
    albums => [
      # The private photo is listed first, so it would be the default cover.
      { slug => 'trip', title => 'Trip', photos => [ 'priv0001', 'pub00001' ] },
    ],
  );

  ok(-e $dir->child('p/pub00001/index.html'), 'public photo has a page');
  like($dir->child('map/photos.geojson')->slurp_raw, qr/pub00001/, 'public photo is on the map');
  like($dir->child('albums/index.html')->slurp_raw, qr/pub00001/, 'public photo is the album cover');
  never_mentioned_ok('private photo', $dir, 'priv0001');
  ok(! -e $dir->child('tags/secret'), 'tag used only by a private photo has no page');
};

subtest 'albums with only private photos are omitted' => sub {
  my ($site, $dir) = built_site(
    photos => [ { id => 'priv0002', visibility => 'private' } ],
    albums => [ { slug => 'hidden', title => 'Hidden', photos => [ 'priv0002' ] } ],
  );

  ok(! -e $dir->child('albums/hidden'), 'no album page');
  never_mentioned_ok('empty album', $dir, 'hidden/');
};

subtest 'private zones' => sub {
  my ($library) = library_with(
    config => qq{[[private_zone]]\nlat = 40.0\nlon = -75.0\nradius = 500\n},
    photos => [],
  );
  my $site = Jiggle::Site->new({ library => $library });

  my sub at ($lat, $lon) {
    Jiggle::Photo->new({
      id => 'x', location => { lat => $lat, lon => $lon },
      original => { ext => 'jpg', width => 1, height => 1 },
    });
  }

  location_published_is('inside zone',  $site, at(40.001, -75.001), undef);
  location_published_is('outside zone', $site, at(40.01, -75.0), { lat => 40.01, lon => -75.0 });
};

subtest 'located photos inside a private zone stay off the map' => sub {
  my ($site, $dir) = built_site(
    config => qq{[[private_zone]]\nlat = 40.0\nlon = -75.0\nradius = 500\n},
    photos => [ { id => 'home0001', location => { lat => 40.0001, lon => -75.0001 } } ],
  );

  unlike($dir->child('map/photos.geojson')->slurp_raw, qr/home0001/, 'not in GeoJSON');
  unlike($dir->child('p/home0001/index.html')->slurp_raw, qr/jiggleMap/, 'no map on page');
};

subtest 'rebuilding changes nothing, and pruning removes the stale' => sub {
  my ($site, $dir, $library) = built_site(
    photos => [ { id => 'aaaa0001' }, { id => 'bbbb0001' } ],
  );

  my $page  = $dir->child('p/aaaa0001/index.html');
  my $mtime = (stat $page)[9];
  utime $mtime - 100, $mtime - 100, $page;

  my $rebuild = Jiggle::Site->new({ library => $library });
  $rebuild->build;

  is((stat $page)[9], $mtime - 100, 'unchanged page was not rewritten');
  is($rebuild->writer->stats->{written}, 0, 'nothing written on rebuild');

  $dir->child('p/zzzz0001/index.html')->touchpath;
  Jiggle::Site->new({ library => $library })->build;
  ok(! -e $dir->child('p/zzzz0001'), 'stale file and its directory pruned');
};

done_testing;
