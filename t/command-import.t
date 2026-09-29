use v5.36;

use Test::More;

use lib 'lib', 't/lib';

use App::Cmd::Tester;
use Jiggle::App;
use Jiggle::TestLibrary;
use Path::Tiny ();

plan skip_all => 'git is needed' unless system('git --version >/dev/null 2>&1') == 0;

# An empty directory is enough: the guard refuses before anything is read.
my $source = Path::Tiny->tempdir;

sub import_result ($library, @args) {
  test_app('Jiggle::App', [ '-L', $library->root, 'import-flickr', @args, '--no-derive', "$source" ]);
}

sub git ($dir, @args) {
  system('git', '-C', "$dir", @args) == 0 or die "git @args failed";
}

subtest 'a library whose meta/ has commits is protected' => sub {
  my ($library) = library_with(photos => [ { id => 'aaaa0001' } ]);
  my $meta = $library->meta_dir;

  git($meta, qw( init --quiet ));
  git($meta, qw( add . ));
  git($meta, qw( -c user.name=test -c user.email=test@example.com commit --quiet -m first ));

  my $refused = import_result($library);
  like($refused->error, qr/meta\/ has git commits/, 'refused without --force');

  my $forced = import_result($library, '--force');
  unlike($forced->error // '', qr/git commits/, 'allowed with --force');
};

subtest 'an uncommitted library is not' => sub {
  my ($library) = library_with(photos => [ { id => 'bbbb0001' } ]);
  git($library->meta_dir, qw( init --quiet ));   # a repository, but no commits

  my $result = import_result($library);
  unlike($result->error // '', qr/git commits/, 'no commits yet: allowed');
};

done_testing;
