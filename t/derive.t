use v5.36;

use Test::More;

use lib 'lib';

use Image::ExifTool ();
use Jiggle::Derive;
use Jiggle::Ingest;
use Jiggle::Library;
use Path::Tiny ();

for my $tool (qw( vips ffmpeg exiftool )) {
  plan skip_all => "$tool is needed to make test media"
    unless system("$tool -ver >/dev/null 2>&1") == 0
        || system("$tool --version >/dev/null 2>&1") == 0
        || system("$tool -version >/dev/null 2>&1") == 0;
}

my $tmp = Path::Tiny->tempdir;

sub run (@cmd) { system(@cmd) == 0 or die "command failed: @cmd\n" }

# A 64x48 JPEG with GPS in its EXIF.
sub jpeg_with_gps {
  my $file = $tmp->child('src/photo.jpg');
  $file->parent->mkpath;
  run('vips', 'black', "$file", 64, 48);
  run(qw( exiftool -q -overwrite_original ),
    '-GPSLatitude=52.5287', '-GPSLatitudeRef=N',
    '-GPSLongitude=13.3712', '-GPSLongitudeRef=E',
    "$file");
  return $file;
}

# A one-second 64x48 clip, flagged as rotated 90 degrees the way a phone
# flags a portrait video, with GPS in both of the places video files keep it:
# Apple's metadata keys (which ffmpeg happens to drop when writing MP4), and
# the older ©xyz location atom used by Android and many cameras (which
# ffmpeg would copy, if not told to strip metadata).
sub rotated_mov_with_gps {
  my $file = $tmp->child('src/clip.mov');
  $file->parent->mkpath;
  run(qw( ffmpeg -nostdin -loglevel error -y ),
    qw( -f lavfi -i testsrc=size=64x48:rate=10:duration=1 ),
    qw( -f lavfi -i sine=duration=1 ),
    qw( -c:v libx264 -pix_fmt yuv420p -c:a aac -shortest ),
    '-metadata', 'location=+52.5287+013.3712/',
    "$file");
  run(qw( exiftool -q -overwrite_original ),
    '-Rotation=90', '-Keys:GPSCoordinates=52.5287 13.3712',
    "$file");
  return $file;
}

sub derived_library ($file) {
  my $root = $tmp->child('lib-' . $file->basename);
  $root->child('jiggle.toml')->touchpath;
  my $library = Jiggle::Library->new({ root => $root });

  my ($photo) = Jiggle::Ingest->new({ library => $library })->ingest_files($file);
  Jiggle::Derive->new({ library => $library, jobs => 1 })->derive_photos($photo);

  return ($library, $photo);
}

sub has_location ($file) {
  my $info = Image::ExifTool::ImageInfo("$file", 'GPS*', 'Location*');
  return scalar grep {; ! /^(Error|Warning)/ } keys %$info;
}

sub size_of ($file) {
  my $info = Image::ExifTool::ImageInfo("$file", qw( ImageWidth ImageHeight Rotation ));
  my ($w, $h) = @$info{qw( ImageWidth ImageHeight )};
  ($w, $h) = ($h, $w) if ($info->{Rotation} // 0) % 180;
  return [ $w, $h ];
}

sub renditions_ok ($desc, $library, $photo, %want) {
  subtest $desc => sub {
    is($photo->type, $want{type}, 'type');
    is_deeply([ $photo->width, $photo->height ], $want{size}, 'upright size recorded');

    my $dir = $library->derived_path($photo->id);

    for my $recipe (Jiggle::Derive->recipes_for($photo)) {
      my $file = $dir->child($recipe->{name});
      ok(-e $file, "$recipe->{name} made") or next;

      next unless $recipe->{publish} // 1;

      ok(! has_location($file), "$recipe->{name} has no location");
      is_deeply(
        size_of($file),
        [ Jiggle::Derive->rendition_size($photo, $recipe->{name}) ],
        "$recipe->{name} has the computed size",
      );
    }

    is_deeply(
      [ sort map {; $_->{name} } Jiggle::Derive->published_recipes_for($photo) ],
      [ sort $want{published}->@* ],
      'published renditions',
    );
  };
}

my @images = qw( h480.webp 500.webp 1024.webp 2048.webp og.jpg );

{
  my $src = jpeg_with_gps();
  ok(has_location($src), 'test photo really has a location');

  renditions_ok('photo', derived_library($src),
    type => 'photo', size => [ 64, 48 ], published => \@images);
}

{
  my $src = rotated_mov_with_gps();
  ok(has_location($src), 'test video really has a location');

  renditions_ok('rotated video', derived_library($src),
    type => 'video', size => [ 48, 64 ], published => [ @images, 'video.mp4' ]);
}

subtest 'an unreadable original is reported, not fatal' => sub {
  my $good = $tmp->child('src/good.jpg');
  my $bad  = $tmp->child('src/bad.jpg');
  $good->parent->mkpath;
  run('vips', 'gaussnoise', "$good", 320, 240);
  run('vips', 'gaussnoise', "$bad",  640, 480);

  my $root = $tmp->child('lib-unreadable');
  $root->child('jiggle.toml')->touchpath;
  my $library = Jiggle::Library->new({ root => $root });
  my ($g, $b) = Jiggle::Ingest->new({ library => $library })->ingest_files($good, $bad);

  # libvips makes the best of most damaged JPEGs, so to be sure of a failure,
  # the original goes missing instead.
  my $original = $library->original_path($b);
  chmod 0644, "$original";
  $original->remove;

  my $derive = Jiggle::Derive->new({ library => $library, jobs => 1 });
  my $ok = eval { $derive->derive_photos($g, $b); 1 };

  ok($ok, 'derive_photos returned') or diag $@;
  is_deeply([ $derive->failed ], [ $b->id ], 'the bad one is reported as failed');
  ok(-e $library->derived_path($g->id, '1024.webp'), 'the good one was still made');
};

subtest 'a damaged original is reported by photo' => sub {
  my $file = $tmp->child('src/truncated.jpg');
  $file->parent->mkpath;
  run('vips', 'gaussnoise', "$file", 256, 192);
  my $bytes = $file->slurp_raw;
  $file->spew_raw(substr $bytes, 0, int(length($bytes) * 0.6));

  my $root = $tmp->child('lib-truncated');
  $root->child('jiggle.toml')->touchpath;
  my $library = Jiggle::Library->new({ root => $root });
  my ($photo) = Jiggle::Ingest->new({ library => $library })->ingest_files($file);

  my $derive = Jiggle::Derive->new({ library => $library, jobs => 1 });
  $derive->derive_photos($photo);

  my %warnings = $derive->warnings;
  ok($warnings{ $photo->id }, 'the photo has warnings');
  like(join("\n", ($warnings{ $photo->id } // [])->@*), qr/premature end of JPEG/i,
    '...saying what went wrong');
};

done_testing;
