use v5.36;
use utf8;

use Test::More;

use lib 'lib', 't/lib';

use Jiggle::TestGo;
use JSON::MaybeXS ();
use Mojo::UserAgent;
use Path::Tiny ();

# This drives the editor that jiggle-go serves, in a real browser, with
# Playwright, which isn't a dependency: set JIGGLE_PLAYWRIGHT to a directory
# where it's installed (with its browsers where it can find them, as by
# PLAYWRIGHT_BROWSERS_PATH).  It needs Go, to build the server, and node.
# -- claude, 2026-11-23
plan skip_all => 'set JIGGLE_PLAYWRIGHT to run browser tests' unless $ENV{JIGGLE_PLAYWRIGHT};
plan skip_all => 'needs node' unless system('node --version >/dev/null 2>&1') == 0;
go_or_skip();

my $root = library_to_migrate(
  photos => [
    { id => 'aaaa0001', title => 'one',   taken => '2026-07-01T10:00:00', tags => [ 'cat', 'pet' ] },
    { id => 'bbbb0002', title => 'two',   taken => '2026-07-02T10:00:00' },
    { id => 'cccc0003', title => 'three', taken => '2026-07-03T10:00:00' },
    { id => 'eeee0001', visibility => 'pending', taken => '2026-09-01T10:00:00' },
    { id => 'eeee0002', visibility => 'pending', taken => '2026-09-02T10:00:00' },
    { id => 'eeee0003', visibility => 'pending', taken => '2026-09-03T10:00:00' },
    { id => 'dddd0001', visibility => 'private' },
  ],
  albums => [
    { slug => 'trip',   title => 'Trip',   cover => 'aaaa0001', photos => [ qw( aaaa0001 bbbb0002 cccc0003 ) ] },
    { slug => 'hidden', title => 'Hidden', photos => [ 'dddd0001' ] },
  ],
);

my $db = scratch_file('db', 'sqlite');
my ($status, $out) = jiggle_go(migrate => '--library', $root, '--out', $db);
is($status, 0, 'migrate worked') or BAIL_OUT($out);

my $url = jiggle_go_serving($db, $root);
my $ua  = Mojo::UserAgent->new;

# Calls one JMAP method, and returns its response's arguments.
sub jmap ($method, $args) {
  my $res = $ua->post("$url/jmap/api" => json => {
    using       => [ 'urn:ietf:params:jmap:core', 'https://semiotic.systems/jmap/jiggle' ],
    methodCalls => [ [ $method, { accountId => 'photos', %$args }, 'a' ] ],
  })->result;
  return $res->json->{methodResponses}[0][1];
}

sub photo ($id) { jmap('Photo/get', { ids => [ $id ] })->{list}[0] }

sub album ($slug) {
  my $ids = jmap('Album/query', { filter => { slug => $slug } })->{ids};
  return @$ids ? jmap('Album/get', { ids => $ids })->{list}[0] : undef;
}

# Imports a file, and returns the photo's id once the import has come to
# something: an original, or an error.
sub imported ($name, $bytes) {
  my $blob = $ua->post("$url/jmap/upload/photos/" => { 'Content-Type' => 'image/jpeg' } => $bytes)
    ->result->json->{blobId};
  my $made = jmap('Photo/import', { photos => { new => { blobId => $blob, name => $name } } });
  my $id   = $made->{created}{new}{id} or BAIL_OUT("can't import $name");

  for (1 .. 300) {
    my $photo = photo($id);
    return $id if $photo->{blobId} or $photo->{importJob}{error};
    select undef, undef, undef, 0.1;
  }
  BAIL_OUT("the import of $name never ended");
}

# Two photos made from real files: one with a real picture, so that its
# renditions can be made again when it's turned, and one cut short, whose
# import fails.  The photos of the test library are a byte each.  Without
# vips there are neither, and that much isn't tested.
sub real_photos {
  return unless grep {; -x "$_/vips" } split /:/, $ENV{PATH};

  my $file = scratch_file('real', 'jpg');
  system('vips', 'black', "$file", 600, 400, '--bands', 3) == 0 or return;

  my $bytes = $file->slurp_raw;
  return (imported('real.jpg', $bytes), imported('broken.jpg', substr $bytes, 0, length($bytes) / 8));
}

my ($real, $broken) = real_photos();

my $script = Path::Tiny::path('t/browser/editor-jmap.mjs');
my $report = `node $script '$url' @{[ $real // '' ]}`;
my $seen = eval { JSON::MaybeXS::decode_json($report) } or BAIL_OUT("no report from the browser: $report");

sub saw ($key, $want) {
  is_deeply($seen->{$key}, $want, $key) or diag explain $seen->{$key};
}

# What the server has for a photo or an album, afterward.
sub photo_has ($id, $field, $want) {
  is_deeply(photo($id)->{$field}, $want, "on the server: ${id}'s $field");
}

sub album_has ($slug, $field, $want) {
  my $album = album($slug) // {};
  is_deeply($album->{$field}, $want, "on the server: ${slug}'s $field");
}

my $true = JSON::MaybeXS::true;

saw(albums       => [ 'Hidden', 'Trip' ]);
saw(album_counts => [ '1 private', '3 published' ]);
saw(album_covers => 2);

saw(album_query        => 'album:trip');
saw(album_order        => [ qw( aaaa0001 bbbb0002 cccc0003 ) ]);
saw(album_order_edited => [ qw( cccc0003 aaaa0001 bbbb0002 ) ]);
saw(album_write_button => 'Write changes (album)');
saw(album_written      => 'written: 1 album(s)');
album_has(trip => photoIds     => [ qw( cccc0003 aaaa0001 bbbb0002 ) ]);
album_has(trip => coverPhotoId => 'cccc0003');

saw(pending_query               => 'pending limit:2');
saw(pending_first               => [ qw( eeee0001 eeee0002 ) ]);
saw(refresh_disabled_when_dirty => $true);
saw(pending_written             => 'written: 1 photo(s)');
saw(pending_next                => [ qw( eeee0002 eeee0003 ) ]);
photo_has(eeee0001 => visibility => 'public');

saw(back_query       => 'album:trip');
saw(bad_query_status => 'unknown query term: pendng');
saw(after_bad_query  => 'album:trip');

saw(tags_edited       => [ qw( dog pet ) ]);
saw(tags_written      => 'written: 1 photo(s) and 1 album(s)');
saw(albums_after      => [ 'Berlin, 2026', 'Trip' ]);
saw(dirty_after_write => 0);
photo_has(aaaa0001 => tags => { dog => $true, pet => $true });
album_has('berlin-2026' => title    => 'Berlin, 2026');
album_has('berlin-2026' => photoIds => [ 'aaaa0001' ]);

saw(pushed_caption => 'deux');

saw(conflict_caption => 'trois');
saw(conflict_note    =>
  'the title was changed elsewhere, to “drei”, while being edited here.  Writing keeps the edit; reverting it takes the other change.');
saw(conflict_tags         => [ 'elsewhere' ]);
saw(conflict_written      => 'written: 1 photo(s)');
saw(conflict_badges_after => [ 'cover' ]);
photo_has(cccc0003 => title => 'trois');
photo_has(cccc0003 => tags  => { elsewhere => $true });

saw(dialogs          => [ "You have changes that aren't written.  Discard them?" ]);
saw(albums_after_new => [ 'Berlin, 2026', 'Hidden', 'Trip' ]);
photo_has(aaaa0001 => title => 'one');

SKIP: {
  skip 'needs vips, to make photos of real files', 6 unless $real;
  saw(waiting_all     => '1 more failed to import');
  saw(waiting_albums  => '');
  saw(turned_written   => 'written: 1 photo(s)');
  saw(turned_badges    => [ 'pending' ]);
  saw(turned_transform => '');
  photo_has($real => rotate => 90);
}

saw(errors => []);

done_testing;
