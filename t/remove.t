use v5.36;

use Test::More;

use lib 'lib', 't/lib';

use Jiggle::Album;
use Jiggle::Derive;
use Jiggle::Ingest;
use Jiggle::Library;
use Jiggle::Remove;
use Jiggle::TestLibrary;
use JSON::MaybeXS ();
use Path::Tiny ();

$ENV{GIT_AUTHOR_NAME}  = $ENV{GIT_COMMITTER_NAME}  = 'Test';
$ENV{GIT_AUTHOR_EMAIL} = $ENV{GIT_COMMITTER_EMAIL} = 'test@example.com';

sub git ($library, @args) {
  my $meta = $library->meta_dir;
  my $out = `git -C '$meta' @args 2>&1`;
  die "git @args: $out" if $?;
  return $out;
}

# A library with its meta/ committed, and an original file for each photo.
sub committed_library (%arg) {
  my ($library) = library_with(%arg);
  for my $photo ($library->photos) {
    my $original = $library->original_path($photo);
    $original->parent->mkpath;
    $original->spew_raw('original bytes');
  }
  git($library, 'init --quiet');
  git($library, 'add .');
  git($library, "commit --quiet -m first");
  return $library;
}

sub remove_from ($library, $ids, %arg) {
  return Jiggle::Remove->new({ library => $library })->remove($ids, \%arg);
}

sub album_of ($library, $slug) {
  Jiggle::Album->from_toml_file($library->albums_dir->child("$slug.toml"));
}

sub in_manifest ($library, $id) {
  my $manifest = JSON::MaybeXS::decode_json($library->derived_dir->child('manifest.json')->slurp_raw);
  return exists $manifest->{photos}{$id};
}

subtest 'removing a photo' => sub {
  my $library = committed_library(
    photos => [ { id => 'gone0001' }, { id => 'keep0001' }, { id => 'keep0002', visibility => 'private' } ],
    albums => [
      { slug => 'trip',  title => 'Trip',  photos => [ 'gone0001', 'keep0001' ] },
      { slug => 'other', title => 'Other', photos => [ 'keep0002' ] },
    ],
  );
  my $original = $library->original_path($library->photo('gone0001'));

  my $result = remove_from($library, [ 'gone0001' ]);

  ok(! -e $library->meta_path('gone0001'), 'its metadata is gone');
  is_deeply(album_of($library, 'trip')->photos, [ 'keep0001' ], "it's out of its album");
  is(album_of($library, 'trip')->cover, 'keep0001', '...whose cover moves to the next photo');
  is_deeply($result->{albums}, [ 'trip' ], 'only that album changed');
  ok(! -d $library->derived_path('gone0001'), 'its renditions are gone');
  ok(! in_manifest($library, 'gone0001'), '...and so is its manifest entry');
  ok(in_manifest($library, 'keep0001'), "others' entries are kept");
  ok(-e $original, 'the original is kept');
  is_deeply($result->{published}, [ 'gone0001' ], 'it was published, which is reported');

  is(git($library, 'log -1 --format=%s'), "remove photo gone0001\n", 'committed');
  is(git($library, 'status --porcelain'), '', '...everything');
  is(git($library, 'show --name-status --format= HEAD'),
     "M\talbums/trip.toml\nD\tgo/gone0001.toml\n", '...and only what changed');

  git($library, 'revert --no-edit HEAD');
  my $again = Jiggle::Library->new({ root => $library->root });
  ok($again->photo('gone0001'), 'reverting the commit brings it back');
  is_deeply(album_of($again, 'trip')->photos, [ 'gone0001', 'keep0001' ], '...in its album');
};

subtest 'removing originals too' => sub {
  my $library = committed_library(photos => [ { id => 'gone0001', visibility => 'pending' }, { id => 'gone0002' } ]);
  my @originals = map {; $library->original_path($library->photo($_)) } qw( gone0001 gone0002 );

  my $result = remove_from($library, [ 'gone0001', 'gone0002' ], originals => 1);

  ok(! -e $_, "deleted: $_") for @originals;
  is_deeply([ map {; "$_" } $result->{originals}->@* ], [ map {; "$_" } @originals ], 'and reported');
  is_deeply($result->{published}, [ 'gone0002' ], 'a pending photo was not published');
  like(git($library, 'log -1 --format=%B'), qr/\Aremove 2 photos\n\ngone0001\ngone0002\n\nTheir originals were deleted too\./,
    'one commit, saying so');
};

subtest 'nothing changes if any id is wrong' => sub {
  my $library = committed_library(photos => [ { id => 'keep0001' } ]);
  ok(! eval { remove_from($library, [ 'keep0001', 'nope0001' ]); 1 }, 'it dies');
  like($@, qr/no photo nope0001/, '...naming the bad id');
  ok(-e $library->meta_path('keep0001'), '...having removed nothing');
};

subtest 'a removed photo can be ingested again' => sub {
  plan skip_all => 'needs vips' unless system('vips --version >/dev/null 2>&1') == 0;

  my $root = Path::Tiny->tempdir;
  $root->child('jiggle.toml')->touchpath->spew_utf8("format = $Jiggle::Library::FORMAT\n");
  my $library = Jiggle::Library->new({ root => $root });

  my $jpeg = $root->child('src.jpg');
  system('vips', 'black', "$jpeg", 9, 8) == 0 or die "vips failed";

  my ($photo) = Jiggle::Ingest->new({ library => $library })->ingest_files($jpeg);
  remove_from($library, [ $photo->id ]);
  ok(-e $library->original_path($photo), 'the original was kept');

  my ($again) = Jiggle::Ingest->new({ library => Jiggle::Library->new({ root => $root }) })->ingest_files($jpeg);
  is($again && $again->id, $photo->id, 'ingesting it again brings it back, with the same id');
};

done_testing;
