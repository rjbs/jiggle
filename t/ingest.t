use v5.36;

use Test::More;

use lib 'lib';

use Digest::SHA ();
use Jiggle::Ingest;
use Jiggle::Library;
use Path::Tiny ();

plan skip_all => 'vips is needed to make test images'
  unless system('vips --version >/dev/null 2>&1') == 0;

my $tmp = Path::Tiny->tempdir;

# Each call makes a distinct small JPEG, by varying its size.
my $n = 0;
sub new_jpeg {
  my $file = $tmp->child('src', sprintf 'IMG_%04d.JPG', ++$n);
  $file->parent->mkpath;
  system('vips', 'black', "$file", 8 + $n, 8) == 0 or die "vips failed";
  return $file;
}

sub new_library {
  my $root = $tmp->child('lib' . ++$n);
  $root->child('jiggle.toml')->touchpath;
  return Jiggle::Library->new({ root => $root });
}

sub ingest_ok ($desc, $library, $files, $want_ids) {
  my @got = map {; $_->id }
            Jiggle::Ingest->new({ library => $library })->ingest_files(@$files);
  is_deeply(\@got, $want_ids, $desc);
}

sub id_of ($file) {
  substr Digest::SHA->new(256)->addfile("$file")->hexdigest, 0, 12;
}

my $a = new_jpeg();
my $b = new_jpeg();

subtest 'ids come from content' => sub {
  my $library = new_library();
  ingest_ok('both ingested, ids are digest prefixes', $library, [ $a, $b ], [ id_of($a), id_of($b) ]);

  my $id = id_of($a);
  ok(-e $library->root->child('originals', substr($id, 0, 2), "$id.jpg"), 'original is in its shard');
  ok(-e $library->meta_path($id), 'metadata written');
};

subtest 're-ingesting skips what is already there' => sub {
  my $library = new_library();
  ingest_ok('first time', $library, [ $a ], [ id_of($a) ]);
  ingest_ok('second time, with a new file too', $library, [ $a, $b ], [ id_of($b) ]);
  ingest_ok('same file twice in one run', new_library(), [ $a, $a ], [ id_of($a) ]);
};

subtest 'an id collision is fatal' => sub {
  my $library = new_library();
  ingest_ok('ingest one file', $library, [ $a ], [ id_of($a) ]);

  # Pretend the existing photo had a different full digest with the same
  # 12-digit prefix.
  my $meta = $library->meta_path(id_of($a));
  my $toml = $meta->slurp_utf8;
  $toml =~ s/^(sha256 = "\p{XDigit}{12})\p{XDigit}{52}"/$1 . ('0' x 52) . '"'/me;
  $meta->spew_utf8($toml);

  my $ok = eval { Jiggle::Ingest->new({ library => $library })->ingest_files($a); 1 };
  ok(! $ok, 'ingest died');
  like($@, qr/id collision/, '...with a collision error');
};

done_testing;
