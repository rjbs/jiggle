use v5.36;
use utf8;

use Test::More;

use lib 'lib', 't/lib';

use Jiggle::Library;
use Jiggle::Site;
use Jiggle::TestGo;

# This tests the site builder in Go against the one in Perl: each builds the
# site from the same library, Go's by way of a migration, and the two sites
# are compared.  A file is identical in both, or is equivalent, which is to
# say the same page written differently, or it's a difference, and there
# must be none.  So it needs Go.

go_or_skip();

# Build a library's site both ways and compare them.  Returns the status and
# output of the comparison.  If there's a "between", it's called with the
# library's root once Perl has built its site, before Go builds its own.
sub compared_sites (%arg) {
  my $between = delete $arg{between};
  my $root = library_to_migrate(%arg);

  Jiggle::Site->new({ library => Jiggle::Library->new({ root => $root }) })->build;
  $between->($root) if $between;

  my $db = scratch_file('db', 'sqlite');
  my ($status, $out) = jiggle_go(migrate => '--library', $root, '--out', $db);
  is($status, 0, 'migrate worked') or diag $out;

  ($status, $out) = jiggle_go(build => '--db', $db, '--library', $root,
    '--out', $root->child('site-go'), '--no-search');
  is($status, 0, 'the Go build worked') or diag $out;

  return jiggle_go(sitediff => '--perl', $root->child('site'), '--go', $root->child('site-go'),
    '--equivalent', '--show', 50);
}

# The two sites must not differ.  Files that are equivalent, and not
# identical, must be the ones given as "equivalent", which by default is
# none.
sub sites_agree ($desc, %arg) {
  my $want = delete $arg{equivalent} // [];

  subtest $desc => sub {
    my ($status, $out) = compared_sites(%arg);
    is($status, 0, 'the sites do not differ') or diag $out;

    my @equivalent = sort $out =~ /^(.+): equivalent$/mg;
    is_deeply(\@equivalent, [ sort @$want ], 'the right files are equivalent, not identical')
      or diag $out;

    my ($files) = $out =~ /^files:\s+(\d+)$/m;
    cmp_ok($files, '>', 10, "...of $files files");
  };
}

sites_agree('an empty library', photos => []);

sites_agree('one photo', photos => [ { id => 'aaaaaaaaaaaa' } ]);

# One of everything the site treats specially.
my @everything = (
  { id => 'aaaaaaaaaaaa', title => q{we've got "legs" & <arms>},
    description => "Two lines,\nand a [link](https://example.com/?a=1&b=2), and a 🦵.\n\n> quoted",
    taken => '2026-07-17T17:23:17+02:00', added => '2026-07-20T09:00:00+02:00',
    tags => [ 'vienna', 'High St', 'rock & roll' ],
    location => { lat => 48.208412, lon => 16.373129 } },
  { id => '000000123456', type => 'video', title => 'Rocky',
    taken => '2026-07-17T15:23:17Z', added => '2026-07-20T07:00:00Z', tags => [ 'vienna' ],
    original => { file => 'clip.mov', ext => 'jpg', sha256 => '0' x 64, bytes => 1,
                  width => 1920, height => 1080, duration => 102.4 } },
  { id => 'bbbbbbbbbbbb', title => 'Turned', rotate => 90, taken => '2026-07-18T08:00:00',
    added => '2026-07-20T09:00:00+02:00', location => { lat => 40.0001, lon => -74.9999 } },
  { id => 'cccccccccccc', title => 'Unlisted', visibility => 'unlisted', taken => '2026-07-18T09:00:00',
    added => '2026-07-21T09:00:00+02:00', tags => [ 'vienna', 'secretive' ],
    location => { lat => 12.345678, lon => 98.765432 } },
  { id => 'dddddddddddd', title => 'Private', visibility => 'private', taken => '2026-07-18T10:00:00',
    added => '2026-07-22T09:00:00+02:00', tags => [ 'furtive' ] },
  { id => 'eeeeeeeeeeee', title => 'Pending', visibility => 'pending', taken => '2026-07-18T11:00:00' },
  { id => 'ffffffffffff', title => 'In a zone', taken => '2025-12-31T23:59:59-05:00',
    flickr_uploaded => '2026-01-01T12:00:00-05:00', location => { lat => 51.5074, lon => -0.1278 } },
  { id => '111111111111', title => 'A private place', taken => '2025-01-01T00:00:00',
    location => { lat => 35.6762, lon => 139.6503, private => 1 } },
  { id => '222222222222', description => 'Untitled and undated.', added => '2026-06-01T12:00:00Z' },
  { id => '333333333333', title => 'Never added, never taken' },
  { id => '555555555555', title => 'Not rendered', taken => '2026-07-19T12:00:00' },
  { id => '666666666666', title => 'Only in an album of its own', visibility => 'unlisted' },
);

# And enough photos to fill a feed, and to make months and years that are
# sampled, not shown whole: loose ones, a few to a day, and two albums' worth.
my @many = map {;
  my $n = $_;
  +{
    id    => sprintf('9%011d', $n),
    title => ($n % 7 ? "Photo $n" : ''),
    taken => sprintf('2024-%02d-%02dT%02d:00:00', 1 + $n % 3, 1 + $n % 28, $n % 24),
    added => sprintf('2024-%02d-%02dT12:00:00Z', 4 + $n % 9, 1 + int($n / 4) % 28),
    tags  => [ $n % 2 ? 'odd' : 'even', ($n % 5 ? () : 'fives') ],
  }
} 1 .. 120;

sites_agree('a library with a bit of everything',
  config => <<~'END',
    title    = "rjbs's \"photos\" & more"
    base_url = "https://photos.example.com/"
    author   = "Some Body"
    location_precision = 2

    [[private_zone]]
    lat    = 51.5
    lon    = -0.12
    radius = 2000
    END
  photos     => [ @everything, @many ],
  unrendered => [ '555555555555' ],
  albums     => [
    { slug => 'trip', title => 'A Trip', created => '2026-07-19T10:00:00+02:00', cover => 'cccccccccccc',
      photos => [ qw( cccccccccccc aaaaaaaaaaaa dddddddddddd 000000123456 aaaaaaaaaaaa ) ] },
    { slug => 'hidden', title => 'Hidden', created => '2026-07-19T08:00:00Z', photos => [ '666666666666' ] },
    { slug => 'secret', title => 'Secret', created => '2026-07-19T08:00:00Z', photos => [ qw( dddddddddddd eeeeeeeeeeee ) ] },
    { slug => 'covered', title => 'covered by a private photo', cover => 'dddddddddddd',
      photos => [ qw( bbbbbbbbbbbb 222222222222 ) ] },
    { slug => 'early', title => 'Early ones', created => '2024-03-01T00:00:00-05:00',
      photos => [ map {; $_->{id} } @many[0 .. 19] ] },
    { slug => 'no-date', title => 'an Album with no date', photos => [ map {; $_->{id} } @many[20 .. 24] ] },
    { slug => 'same-moment', title => 'Zebra', created => '2024-03-01T05:00:00Z', photos => [ $many[30]{id} ] },
    { slug => 'empty', title => 'Empty', photos => [] },
  ],
);

# html/template writes a character outside ASCII in a URL as its
# percent-encoding, where Perl jiggle writes the character.  Both are the
# same link.  These are the pages with such a link.
sites_agree('a tag that is not ASCII',
  photos => [
    { id => 'aaaaaaaaaaaa', tags => [ 'søren', 'café society' ], taken => '2026-07-17T17:23:17' },
    { id => 'bbbbbbbbbbbb', tags => [ 'vienna' ], taken => '2026-07-18T17:23:17' },
  ],
  equivalent => [ 'p/aaaaaaaaaaaa/index.html', 'tags/index.html' ],
);

# The comparison has to be able to fail.  Here the Perl site is built, and
# then the library is changed before it's migrated, so the Go site is of a
# different library.
subtest 'a difference is noticed' => sub {
  my ($status, $out) = compared_sites(
    photos => [ { id => 'aaaaaaaaaaaa', title => 'Before' }, { id => 'bbbbbbbbbbbb' } ],
    between => sub ($root) {
      my $meta = $root->child('meta/aa/aaaaaaaaaaaa.toml');
      $meta->spew_utf8($meta->slurp_utf8 =~ s/Before/After/r);
      $root->child('meta/bb/bbbbbbbbbbbb.toml')->remove;
    },
  );

  isnt($status, 0, 'the comparison failed');
  like($out, qr{^p/aaaaaaaaaaaa/index\.html: Perl has .*Before.* where Go has .*After}m, '...at the changed page');
  like($out, qr{^p/bbbbbbbbbbbb/index\.html: only in Perl$}m, '...and at the page only one has');
};

done_testing;
