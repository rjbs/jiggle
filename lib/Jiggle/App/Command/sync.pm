package Jiggle::App::Command::sync;
use v5.36;

use Jiggle::App -command;

sub abstract { 'copy the built site to the web server' }

sub description {
  <<~'END';
  This copies site/ to the [publish] target in jiggle.toml with rsync:

    [publish]
    target = "user@example.com:/var/www/photos.example.com/"
    rsync  = "rsync"      # optional: which rsync to run

  Files removed from site/ are removed from the target too, which is how a
  photo made private leaves the published site.  Unchanged files are skipped
  by size and modification time; builds leave unchanged files alone, so
  that's accurate, and much faster than comparing contents.

  It refuses to run after an interrupted build, whose output may be half
  updated.  Build again first.
  END
}

sub opt_spec {
  return (
    [ 'dry-run|n', 'show what would be copied or removed, but change nothing' ],
  );
}

sub execute ($self, $opt, $args) {
  my $library = $self->library;
  my $publish = $library->config->{publish} // {};

  my $target = $publish->{target}
    or die "no [publish] target in jiggle.toml; see: jiggle help sync\n";

  my $site = $library->root->child('site');
  die "no site at $site; run jiggle build first\n" unless -d $site;

  my $marker = $library->state_dir->child('site-manifest.json.building');
  die "the last build didn't finish, so site/ may be half updated; build again first\n"
    if -e $marker;

  my $rsync = $publish->{rsync} // 'rsync';

  # macOS's own rsync is openrsync, which has only the basic options.  With
  # rsync 3, deletions wait until the new files are in place, so the live
  # site never links to pages already gone, and progress is one line and a
  # summary rather than a list of every file.
  my $modern = `$rsync --version 2>/dev/null` =~ /\Arsync\s+version\s+3\./m;

  my @cmd = (
    $rsync,
    '--recursive', '--links', '--times', '--compress',
    ($modern ? ('--delete-delay', '--info=progress2,stats1', '--human-readable')
             : ('--delete', '--verbose')),
    ($opt->dry_run ? ('--dry-run', ($modern ? '--itemize-changes' : ())) : ()),
    "$site/",
    $target,
  );

  $self->logger->("@cmd");
  system(@cmd) == 0 or die "rsync failed\n";
  $self->logger->($opt->dry_run ? 'dry run done; nothing changed' : 'synced');
}

1;
