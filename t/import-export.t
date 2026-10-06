use v5.36;

use Test::More;

use lib 'lib';

use JSON::MaybeXS ();
use Jiggle::Import::FlickrExport;
use Jiggle::Library;
use Path::Tiny ();
use Time::Local ();

for my $tool (qw( vips exiftool )) {
  plan skip_all => "$tool is needed to make test images"
    unless system("$tool -ver >/dev/null 2>&1") == 0
        || system("$tool --version >/dev/null 2>&1") == 0;
}

my $tmp  = Path::Tiny->tempdir;
my $JSON = JSON::MaybeXS->new->utf8->canonical;

# Put one photo into a fake export: a record in metadata/1, and a small JPEG
# (distinct per $pixels) under photos/1, named as the export names it.  With
# orientation, the JPEG gets that EXIF orientation.
sub export_photo ($root, %arg) {
  my $id = $arg{id};

  my $meta = $root->child('metadata', '1', "photo_$id.json");
  $meta->parent->mkpath;
  $meta->spew_raw($JSON->encode({
    id            => "$id",
    name          => $arg{name} // '',
    description   => $arg{description} // '',
    date_taken    => $arg{taken} // '2008-01-06 19:36:11',
    date_imported => $arg{imported} // '2008-01-06 18:32:33',
    privacy       => $arg{privacy} // 'public',
    rotation      => $arg{rotation} // 0,
    tags          => [ map {; { tag => $_ } } ($arg{tags} // [])->@* ],
    geo           => $arg{geo} ? [ $arg{geo} ] : [],
    albums        => [],
  }));

  return unless $arg{pixels};

  my $name = $arg{file} // "slug_${id}_o.jpg";
  my $jpg  = $root->child('photos', '1', $name);
  $jpg->parent->mkpath;
  system('vips', 'black', "$jpg", $arg{pixels}, 30) == 0 or die "vips failed";

  if ($arg{orientation}) {
    system(qw( exiftool -q -overwrite_original -n ), "-Orientation=$arg{orientation}", "$jpg") == 0
      or die "exiftool failed";
  }

  return;
}

sub export_albums ($root, @albums) {
  $root->child('metadata', '1', 'albums.json')->spew_raw($JSON->encode({ albums => \@albums }));
}

sub imported ($root) {
  my $lib = $tmp->child('lib-' . $root->basename);
  $lib->child('jiggle.toml')->touchpath->spew_utf8("format = $Jiggle::Library::FORMAT\n");
  my $library = Jiggle::Library->new({ root => $lib });

  my $summary = Jiggle::Import::FlickrExport->new({ library => $library, root => $root })->run;

  my $reloaded = Jiggle::Library->new({ root => $lib });
  my %by_flickr = map {; $_->flickr_id => $_ } $reloaded->photos;
  return ($summary, \%by_flickr, [ $reloaded->albums ]);
}

sub photo_is ($desc, $photos, $flickr_id, %want) {
  my $photo = $photos->{$flickr_id};
  ok($photo, "$desc: photo $flickr_id imported") or return;
  for my $key (sort keys %want) {
    is_deeply($photo->$key, $want{$key}, "$desc: $key");
  }
}

sub epoch_of ($datetime) {
  my ($y, $mo, $d, $h, $mi, $s, $sign, $oh, $om)
    = $datetime =~ /\A(\d+)-(\d+)-(\d+)T(\d+):(\d+):(\d+)([-+])(\d+):(\d+)\z/ or return;
  my $offset = ($sign eq '-' ? -1 : 1) * ($oh * 3600 + $om * 60);
  return Time::Local::timegm($s, $mi, $h, $d, $mo - 1, $y) - $offset;
}

subtest 'photo metadata' => sub {
  my $root = $tmp->child('export1');

  export_photo($root, id => 2173311823, pixels => 11,
    name => "we've got legs", description => 'we <b>know</b>',
    tags => [ 'high-st' ], geo => { latitude => '40623775', longitude => '-75373222', accuracy => '15' },
    taken => '2008-01-06 19:36:11', imported => '2008-07-04 16:12:06');

  export_photo($root, id => 3001, pixels => 12, privacy => 'friend & family');

  export_photo($root, id => 18099963404, pixels => 13, file => '18099963404_cb18059961_o.jpg');

  export_photo($root, id => 3002);   # no original in the export

  my ($summary, $photos) = imported($root);

  is($summary->{imported}, 3, 'three imported');
  is_deeply($summary->{skipped}, [ '3002: no original in the export' ], 'the one without an original is skipped');

  photo_is('mapped fields', $photos, 2173311823,
    title       => "we've got legs",
    description => 'we **know**',
    tags        => [ 'high-st' ],
    taken       => '2008-01-06T19:36:11',
    location    => { lat => 40.623775, lon => -75.373222 },
    visibility  => 'public');

  # 16:12:06 on a Pacific clock, in July, is 23:12:06 UTC.
  is(epoch_of($photos->{2173311823}->flickr_uploaded),
     Time::Local::timegm(6, 12, 23, 4, 6, 2008),
     'upload time was Pacific time');
  is($photos->{2173311823}->added, $photos->{2173311823}->flickr_uploaded,
    'added is the upload time');

  photo_is('friends and family is private', $photos, 3001, visibility => 'private');
  photo_is('an untitled photo is found by its id', $photos, 18099963404, title => '');
};

subtest 'rotation' => sub {
  my $root = $tmp->child('export2');

  # EXIF says to turn it 90 degrees, and Flickr shows it turned 90: nothing
  # more to do.
  export_photo($root, id => 4001, pixels => 40, orientation => 6, rotation => 90);

  # EXIF says nothing, but it was turned by hand on Flickr.
  export_photo($root, id => 4002, pixels => 41, orientation => 1, rotation => 90);

  # EXIF says 90, but Flickr shows 270: turned 180 more by hand.
  export_photo($root, id => 4003, pixels => 42, orientation => 6, rotation => 270);

  my (undef, $photos) = imported($root);

  photo_is('rotation from EXIF alone', $photos, 4001, rotate => 0, width => 30, height => 40);
  photo_is('turned by hand',           $photos, 4002, rotate => 90, width => 30, height => 41);
  photo_is('EXIF, then by hand',       $photos, 4003, rotate => 180, width => 30, height => 42);
};

subtest 'video rotation' => sub {
  plan skip_all => 'ffmpeg is needed to make a test video'
    unless system('ffmpeg -version >/dev/null 2>&1') == 0;

  my $root = $tmp->child('export-video');

  # Flickr's record says 0 for every video, whatever the video's own
  # rotation flag says; here, the flag says it's a portrait clip.
  export_photo($root, id => 7001, rotation => 0);

  my $mov = $root->child('photos', '1', "portrait-clip_7001.mov");
  $mov->parent->mkpath;
  system(qw( ffmpeg -nostdin -loglevel error -y -f lavfi -i testsrc=size=64x48:rate=10:duration=1 ),
    qw( -c:v libx264 -pix_fmt yuv420p ), "$mov") == 0 or die "ffmpeg failed";
  system(qw( exiftool -q -overwrite_original -Rotation=90 ), "$mov") == 0 or die "exiftool failed";

  my (undef, $photos) = imported($root);
  photo_is('only its own flag turns it', $photos, 7001,
    type => 'video', rotate => 0, width => 48, height => 64);
};

subtest 'albums' => sub {
  my $root = $tmp->child('export3');

  export_photo($root, id => $_, pixels => $_ - 4980) for 5001, 5002, 5003;
  export_albums($root,
    {
      id => '72157603651676472', title => 'dining table, 2008-01', description => 'a table',
      photos => [ '5003', '5001', '9999', '5002' ],   # 9999 isn't in the export
      cover_photo => 'https://www.flickr.com/photos/rjbs/5001',
      created => '1199674166',
    },
    { id => '72157600000000001', title => 'empty', photos => [ '9999' ] },
  );

  my (undef, $photos, $albums) = imported($root);

  is(scalar @$albums, 1, 'one album; the one with no imported photos is left out');
  my ($album) = @$albums;
  is($album->title, 'dining table, 2008-01', 'title');
  is_deeply($album->photos, [ map {; $photos->{$_}->id } 5003, 5001, 5002 ], 'order kept, missing photo dropped');
  is($album->cover, $photos->{5001}->id, 'cover from the export');
  is($album->flickr_id, '72157603651676472', 'flickr id recorded');

  # 1199674166 is 2008-01-07 02:49:26 UTC.
  is(epoch_of($album->created), 1199674166, 'creation time recorded');

  my $flickr = $tmp->child('lib-export3', 'meta', 'flickr');
  is($flickr->child('5001.json')->slurp_raw,
     $root->child('metadata', '1', 'photo_5001.json')->slurp_raw,
     "each photo's Flickr record is kept, byte for byte");
  ok(-e $flickr->child('albums.json'), 'and so is albums.json');
};

subtest 'recognizing an export' => sub {
  my $root = $tmp->child('export4');
  export_photo($root, id => 6001);
  ok(Jiggle::Import::FlickrExport->looks_like_export($root), 'an export');
  ok(! Jiggle::Import::FlickrExport->looks_like_export($tmp->child('export4', 'photos')), 'not an export');
};

done_testing;
