package Jiggle::App::Command::sync;
use v5.36;

use Jiggle::App -command;

sub abstract { 'copy the built site to the web server' }

sub description {
  <<~'END';
  This publishes site/ to the target configured in jiggle.toml.  That's a
  web server reached by rsync:

    [publish]
    target = "user@example.com:/var/www/photos.example.com/"
    rsync  = "rsync"      # optional: which rsync to run

  or a Bunny storage zone, served by a pull zone:

    [publish.bunny]
    zone         = "my-photos"
    storage_url  = "https://ny.storage.bunnycdn.com"  # by the zone's region
    pull_zone_id = 12345
    purge_hosts  = [ "photos.example.com" ]  # default: base_url's host

  Bunny needs two secrets, the storage zone's password and the account's
  API key (for purging the CDN's cache).  They're read from the
  environment, as BUNNY_STORAGE_PASSWORD and BUNNY_API_KEY, or else from
  password and api_key in [publish.bunny].

  If both are configured, choose one with --to.

  Files removed from site/ are removed from the target too, which is how a
  photo made private leaves the published site.  With rsync, unchanged
  files are skipped by size and modification time; builds leave unchanged
  files alone, so that's accurate, and much faster than comparing contents.
  With Bunny, they're skipped by comparing the build's manifest with a
  record of what was uploaded.  Then what changed is purged from the CDN's
  cache, by prefix where much changed; --purge-all purges everything
  instead.  See Jiggle::Sync::Bunny.

  It refuses to run after an interrupted build, whose output may be half
  updated.  Build again first.
  END
}

sub opt_spec {
  return (
    [ 'to=s',      'which target to sync to, if more than one: rsync or bunny' ],
    [ 'dry-run|n', 'show what would be copied or removed, but change nothing' ],
    [ 'purge-all', 'with bunny, purge the whole pull zone from the CDN, not just what changed' ],
  );
}

sub execute ($self, $opt, $args) {
  my $library = $self->library;
  my $publish = $library->config->{publish} // {};

  my @targets = grep {; $_ eq 'rsync' ? $publish->{target} : $publish->{bunny} } qw( rsync bunny );
  die "nothing to sync to: no [publish] target or [publish.bunny] in jiggle.toml; see: jiggle help sync\n"
    unless @targets;

  my $to = $opt->to;
  if (defined $to) {
    die "--to must be rsync or bunny\n" unless $to =~ /\A(?:rsync|bunny)\z/;
    die "no $to target in jiggle.toml; see: jiggle help sync\n" unless grep {; $_ eq $to } @targets;
  } else {
    die "both rsync and bunny targets are configured; choose one with --to\n" if @targets > 1;
    $to = $targets[0];
  }

  my $site = $library->root->child('site');
  die "no site at $site; run jiggle build first\n" unless -d $site;

  my $marker = $library->state_dir->child('site-manifest.json.building');
  die "the last build didn't finish, so site/ may be half updated; build again first\n"
    if -e $marker;

  $to eq 'rsync' ? $self->_rsync($opt, $publish, $site) : $self->_bunny($opt, $publish->{bunny}, $site);
}

sub _rsync ($self, $opt, $publish, $site) {
  my $target = $publish->{target};
  my $rsync  = $publish->{rsync} // 'rsync';

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

sub _bunny ($self, $opt, $config, $site) {
  require Jiggle::Sync::Bunny;
  require Mojo::URL;

  my $library = $self->library;

  my %secret = (
    password => $ENV{BUNNY_STORAGE_PASSWORD} // $config->{password},
    api_key  => $ENV{BUNNY_API_KEY}          // $config->{api_key},
  );
  # A dry run makes no requests, so it needs no secrets.
  unless ($opt->dry_run) {
    die "no Bunny storage password: set BUNNY_STORAGE_PASSWORD\n" unless length($secret{password} // '');
    die "no Bunny API key: set BUNNY_API_KEY\n" unless length($secret{api_key} // '');
  }
  $_ //= '' for values %secret;

  for my $key (qw( zone pull_zone_id )) {
    die "no $key in [publish.bunny]; see: jiggle help sync\n" unless length($config->{$key} // '');
  }

  my $hosts = $config->{purge_hosts} // do {
    my $host = Mojo::URL->new($library->config->{base_url} // '')->host;
    die "no purge_hosts in [publish.bunny], and no base_url to take one from\n" unless $host;
    [ $host ];
  };

  my $sync = Jiggle::Sync::Bunny->new({
    site_dir     => $site,
    state_dir    => $library->state_dir,
    zone         => $config->{zone},
    pull_zone_id => $config->{pull_zone_id},
    purge_hosts  => $hosts,
    ($config->{storage_url} ? (storage_url => $config->{storage_url}) : ()),
    %secret,
    logger => $self->logger,
  });

  my $result = $sync->sync({ dry_run => $opt->dry_run, purge_all => $opt->purge_all });

  if ($opt->dry_run) {
    say "upload $_" for $result->{uploaded}->@*;
    say "delete $_" for $result->{deleted}->@*;
    $self->logger->(sprintf 'dry run done; would upload %d and delete %d, then purge them',
      0 + $result->{uploaded}->@*, 0 + $result->{deleted}->@*);
    return;
  }

  $self->logger->(sprintf 'synced: %d uploaded, %d deleted; %s',
    0 + $result->{uploaded}->@*, 0 + $result->{deleted}->@*,
    $result->{purged} eq 'all' ? 'purged the whole pull zone' : "$result->{purged} purge request(s)");
}

1;
