use v5.36;
use utf8;

use Test::More;

use lib 'lib', 't/lib';

use App::Cmd::Tester;
use Jiggle::App;
use Jiggle::TestLibrary;
use Path::Tiny ();

# This tests the migration to the Go server's database, by the check that
# will be run on the real library: migrate a library, and compare the
# database with what Perl jiggle loads.  So it needs Go.

plan skip_all => 'go is not installed' unless grep {; -x "$_/go" } split /:/, $ENV{PATH};

my $TMP = Path::Tiny->tempdir;
my $GO  = $TMP->child('jiggle-go');

system('go', 'build', '-o', "$GO", './cmd/jiggle-go') == 0
  or BAIL_OUT("can't build jiggle-go");

my $N = 0;

# Run jiggle-go, returning its exit status and everything it printed.
sub jiggle_go (@args) {
  my $out = $TMP->child('out-' . $N++);
  my $status = system("$GO @args > $out 2>&1");
  return ($status >> 8, $out->slurp_utf8);
}

# Make a library, with a one-byte original for each photo, since migrate
# insists on originals, and with a digest of its own for each, since no two
# photos can have one original.  Returns the library's root.
sub library_to_migrate (%arg) {
  $arg{photos} = [
    map {;
      +{
        original => {
          file => "$_->{id}.jpg", ext => 'jpg', bytes => 1, width => 4000, height => 3000,
          sha256 => $_->{id} . '0' x (64 - length $_->{id}),
        },
        %$_,
      }
    } $arg{photos}->@*
  ];

  my ($library, $root) = library_with(%arg);
  for my $photo ($library->photos) {
    my $file = $library->original_path($photo);
    $file->parent->mkpath;
    $file->spew_raw('x');
  }
  return $root;
}

# Migrate the library at $root, and check the database against what
# "jiggle dump" prints of it once $change, if there is one, has been made
# to the library.  Returns check's exit status and output.
sub migrate_and_check ($root, $change = undef) {
  my $db = $TMP->child('db-' . $N++ . '.sqlite');

  my ($status, $out) = jiggle_go(migrate => '--library', $root, '--out', $db);
  is($status, 0, 'migrate worked') or diag $out;

  $change->($root) if $change;

  my $dump = $TMP->child('dump-' . $N++ . '.json');
  my $result = test_app('Jiggle::App', [ '-L', "$root", 'dump' ]);
  is($result->error, undef, 'dump worked');
  $dump->spew_raw($result->stdout);

  return jiggle_go(check => '--db', $db, '--dump', $dump);
}

sub migration_agrees ($desc, %arg) {
  subtest $desc => sub {
    my ($status, $out) = migrate_and_check(library_to_migrate(%arg));
    is($status, 0, 'check found no difference') or diag $out;
    like($out, qr/the database and Perl jiggle agree/, '...and says so');
  };
}

# The check has to be able to fail.  Here the library is changed after it's
# migrated, so the database is right about the library as it was, and wrong
# about what Perl jiggle now loads.
sub change_is_noticed ($desc, $change, @want) {
  subtest $desc => sub {
    my $root = library_to_migrate(
      photos => [
        { id => 'aaaaaaaaaaaa', title => 'Before', tags => [ 'one', 'two' ] },
        { id => 'bbbbbbbbbbbb' },
      ],
      albums => [ { slug => 'both', title => 'Both', photos => [ qw( aaaaaaaaaaaa bbbbbbbbbbbb ) ] } ],
    );

    my ($status, $out) = migrate_and_check($root, $change);
    isnt($status, 0, 'check failed');
    like($out, $_, "...and said $_") for @want;
  };
}

sub rewrite ($file, $code) {
  local $_ = $file->slurp_utf8;
  $code->();
  $file->spew_utf8($_);
}

migration_agrees('a plain library',
  photos => [ { id => 'aaaaaaaaaaaa' } ],
);

migration_agrees('a library with a bit of everything',
  config => <<~'END',
    title    = "Photos"
    base_url = "https://photos.example.com/"
    author   = "Some Body"
    location_precision = 2

    [[private_zone]]
    lat    = 40.0
    lon    = -75.0
    radius = 500

    [publish.bunny]
    zone = "jiggle"
    END
  photos => [
    {
      id          => '3f9a0c21b7e4',
      title       => q{we've got "legs"},
      description => "Two lines,\nand a 🦵.",
      taken       => '2008-01-06T19:36:11',
      tags        => [ 'High-St', 'café', 'high-st' ],
      visibility  => 'unlisted',
      rotate      => 90,
      added       => '2026-09-30T10:15:00-04:00',
      flickr_id   => '2173311823',
      flickr_uploaded => '2008-01-06T21:32:33-05:00',
      location    => { lat => 40.623775, lon => -75.373222, private => 1 },
    },
    {
      id    => '000000123456',
      type  => 'video',
      taken => '2018-12-25T12:20:35Z',
      original => {
        file => 'clip.mov', ext => 'jpg', sha256 => '0' x 64, bytes => 1,
        width => 1920, height => 1080, duration => 7.165,
        source_mtime => '2026-07-19T17:49:45-04:00',
      },
    },
    { id => '123e45678901', taken => '2018-12-25T12:20:35+00:00', visibility => 'private' },
    { id => 'aaaaaaaaaaaa', taken => '2018-07-17T17:23:17+02:00', visibility => 'pending',
      location => { lat => -33.8688, lon => 151.2093 } },
    { id => 'bbbbbbbbbbbb' },
  ],
  unrendered => [ 'bbbbbbbbbbbb' ],
  albums => [
    { slug => 'legs', title => 'Legs', created => '2008-01-06T21:49:26-05:00', cover => '000000123456',
      photos => [ qw( aaaaaaaaaaaa 3f9a0c21b7e4 aaaaaaaaaaaa 000000123456 ) ] },
    { slug => 'empty', title => 'Empty', photos => [] },
  ],
);

change_is_noticed('a title',
  sub ($root) { rewrite($root->child('meta/aa/aaaaaaaaaaaa.toml'), sub { s/Before/After/ }) },
  qr/photo aaaaaaaaaaaa: title: Perl jiggle has "After", and the database has "Before"/,
);

change_is_noticed('a tag',
  sub ($root) { rewrite($root->child('meta/aa/aaaaaaaaaaaa.toml'), sub { s/"two"/"three"/ }) },
  qr/photo aaaaaaaaaaaa: tags: Perl jiggle has \["one","three"\], and the database has \["one","two"\]/,
);

change_is_noticed(q{an album's order},
  sub ($root) { rewrite($root->child('meta/albums/both.toml'), sub { s/"aaaaaaaaaaaa", "bbbbbbbbbbbb"/"bbbbbbbbbbbb", "aaaaaaaaaaaa"/ }) },
  qr/album both: photos: Perl jiggle has \["bbbbbbbbbbbb","aaaaaaaaaaaa"\]/,
);

change_is_noticed('a photo removed',
  sub ($root) {
    $root->child('meta/bb/bbbbbbbbbbbb.toml')->remove;
    rewrite($root->child('meta/albums/both.toml'), sub { s/, "bbbbbbbbbbbb"// });
  },
  qr/photo bbbbbbbbbbbb: the database has it, and Perl jiggle doesn't/,
  qr/album both: photos: /,
);

change_is_noticed('an album added',
  sub ($root) { $root->child('meta/albums/new.toml')->spew_utf8(qq{title = "New"\nphotos = []\n}) },
  qr/album new: Perl jiggle has it, and the database doesn't/,
);

change_is_noticed('renditions lost',
  sub ($root) { $root->child('derived/manifest.json')->spew_utf8('{"version":1,"photos":{}}') },
  qr/photo aaaaaaaaaaaa: rendered: Perl jiggle has false, and the database has true/,
);

change_is_noticed('the configuration',
  sub ($root) { $root->child('jiggle.toml')->append_utf8(qq{title = "Other"\n}) },
  qr/config: title: Perl jiggle has "Other", and the database has null/,
);

done_testing;
