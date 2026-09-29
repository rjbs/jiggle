use v5.36;

use Test::More;
use Test::Deep;

use lib 'lib';

use Jiggle::Derive;
use Jiggle::Photo;
use Path::Tiny ();

sub photo (%arg) {
  Jiggle::Photo->new({
    id       => 'abcd1234',
    original => {
      file => 'IMG_0001.JPG', ext => 'jpg', sha256 => 'f' x 64,
      bytes => 1000, width => 4000, height => 3000,
    },
    %arg,
  });
}

sub round_trips_ok ($desc, %arg) {
  my $photo = photo(%arg);

  my $file = Path::Tiny->tempfile(SUFFIX => '.toml');
  $file->spew_utf8($photo->as_toml);

  my $reloaded = Jiggle::Photo->from_toml_file($file);

  # Compare everything but the object identity.
  cmp_deeply({ %$reloaded }, { %$photo }, "round trip: $desc")
    or diag $photo->as_toml;
}

sub rendition_size_is ($desc, $w, $h, $rendition, $want) {
  my $photo = photo(original => {
    file => 'x.jpg', ext => 'jpg', sha256 => 'f' x 64, bytes => 1,
    width => $w, height => $h,
  });

  is_deeply(
    [ Jiggle::Derive->rendition_size($photo, $rendition) ],
    $want,
    "rendition size: $desc",
  );
}

round_trips_ok('minimal');

round_trips_ok('everything',
  title       => 'Stephansdom',
  description => 'A church.',
  taken       => '2026-07-17T17:23:17+02:00',
  tags        => [ 'vienna', 'church' ],
  visibility  => 'private',
  flickr_id   => '53012345678',
  flickr_uploaded => '2008-01-06T21:32:33-05:00',
  location    => { lat => 48.2084, lon => 16.3731 },
);

round_trips_ok('local datetime', taken => '2026-07-17T17:23:17');

round_trips_ok('extra rotation', rotate => 90);

round_trips_ok('when it was added', added => '2026-09-29T08:15:00-04:00');

round_trips_ok('source mtime', original => {
  file => 'IMG_9971.JPG', ext => 'jpg', sha256 => 'f' x 64,
  bytes => 1000, width => 4000, height => 3000,
  source_mtime => '2026-07-19T17:49:45-04:00',
});

round_trips_ok('awkward strings',
  title       => qq{"Quoted" \\ back/slash \x{263A}},
  description => qq{Line one,\nline "two" and ""three"",\n\\n is literal\nends with "},
  tags        => [ q{it's}, q{"q"} ],
);

round_trips_ok('southern hemisphere', location => { lat => -37.8098306, lon => -144.9615472 });

round_trips_ok('private location', location => { lat => 48.2084, lon => 16.3731, private => 1 });

round_trips_ok('private location', location => { lat => 48.2084, lon => 16.3731, private => 1 });

rendition_size_is('landscape box',    4000, 3000, '1024.webp',  [ 1024, 768 ]);
rendition_size_is('portrait box',     3000, 4000, '2048.webp',  [ 1536, 2048 ]);
rendition_size_is('never enlarged',    800,  600, '2048.webp',  [ 800, 600 ]);
rendition_size_is('rounds to nearest', 5712, 4284, '500.webp',  [ 500, 375 ]);
rendition_size_is('row, landscape',    5712, 4284, 'h480.webp', [ 640, 480 ]);
rendition_size_is('row, portrait',     4284, 5712, 'h480.webp', [ 360, 480 ]);
rendition_size_is('row, panorama',    10000, 1000, 'h480.webp', [ 1920, 192 ]);

sub rotated_size_is ($desc, $w, $h, $rotate, $rendition, $want) {
  my $photo = photo(rotate => $rotate, original => {
    file => 'x.jpg', ext => 'jpg', sha256 => 'f' x 64, bytes => 1,
    width => $w, height => $h,
  });
  is_deeply([ Jiggle::Derive->rendition_size($photo, $rendition) ], $want, "rotated size: $desc");
}

rotated_size_is('turned a quarter', 4000, 3000,  90, '1024.webp', [ 768, 1024 ]);
rotated_size_is('turned a half',    4000, 3000, 180, '1024.webp', [ 1024, 768 ]);
rotated_size_is('turned back',      4000, 3000, 270, 'h480.webp', [ 360, 480 ]);

done_testing;
