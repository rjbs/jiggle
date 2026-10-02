package Jiggle::Sync::Bunny;
use v5.36;

use Moo;

use Digest::SHA ();
use JSON::MaybeXS ();
use Jiggle::Progress;
use Mojo::Asset::File;
use Mojo::Promise;
use Mojo::URL;
use Mojo::UserAgent;
use Mojo::Util ();
use Path::Tiny ();

=head1 NAME

Jiggle::Sync::Bunny - publish the site to a Bunny storage zone, and purge its CDN

=head1 SYNOPSIS

  my $sync = Jiggle::Sync::Bunny->new({
    site_dir => $library->root->child('site'),
    state_dir => $library->state_dir,
    zone     => 'jiggle-photos',
    password => $storage_password,
    storage_url  => 'https://ny.storage.bunnycdn.com',
    api_key      => $account_api_key,
    pull_zone_id => 12345,
    purge_hosts  => [ 'photos.example.com' ],
  });

  my $result = $sync->sync;

=head1 DESCRIPTION

The site is published by copying F<site/> into a Bunny storage zone, through
Bunny's storage API, and a pull zone serves it.

What to copy isn't found by comparing files with the storage zone, which
would mean listing a directory per photo.  Instead, the build's site
manifest (see L<Jiggle::Site::Writer>) says what each path in F<site/>
holds, and this keeps a record, in the state directory, of what each path
held when it was last uploaded.  A path whose entries differ is uploaded; a
path in the record and not the manifest is deleted.  The record is updated
as each upload or deletion succeeds, so an interrupted sync picks up where
it stopped.  Anything put in the zone by other means is left alone.

Bunny serves F<bunnycdn_errors/404.html> for a missing path, so the site's
F<404.html> is uploaded there too.

Every deletion waits until every upload has succeeded, so the live site
never links to something already gone.  If any upload fails, nothing is
deleted.

Then the CDN's cache is purged of every URL changed or deleted, on each of
C<purge_hosts>.  That's required, not just tidy: a rendition's URL stays the
same when it's remade (say, rotated), and a photo made private must stop
being served from the cache, not just from storage.  A directory's
F<index.html> is served at three URLs (C<dir/>, C<dir>, and
C<dir/index.html>), and all three are purged.  When more than
C<purge_all_over> files changed, the whole pull zone is purged instead,
which is one request rather than thousands.

=cut

has site_dir  => (is => 'ro', required => 1, coerce => sub ($p) { Path::Tiny::path($p) });
has state_dir => (is => 'ro', required => 1, coerce => sub ($p) { Path::Tiny::path($p) });

has zone        => (is => 'ro', required => 1);
has password    => (is => 'ro', required => 1);
has storage_url => (is => 'ro', default => 'https://storage.bunnycdn.com');

has api_key      => (is => 'ro', required => 1);
has pull_zone_id => (is => 'ro', required => 1);
has purge_hosts  => (is => 'ro', required => 1);
has api_url      => (is => 'ro', default => 'https://api.bunny.net');

has concurrency    => (is => 'ro', default => 8);
has attempts       => (is => 'ro', default => 3);
has retry_delay    => (is => 'ro', default => 2);
has purge_all_over => (is => 'ro', default => 500);

has logger => (is => 'ro', default => sub { sub { } });

has ua => (
  is => 'lazy',
  default => sub { Mojo::UserAgent->new(connect_timeout => 30, inactivity_timeout => 120) },
);

my $JSON = JSON::MaybeXS->new->canonical->utf8;

sub _manifest_file ($self) { $self->state_dir->child('site-manifest.json') }
sub _record_file   ($self) { $self->state_dir->child('bunny-uploaded.json') }

sub _load ($self, $file) {
  return {} unless -e $file;
  my $data = $JSON->decode($file->slurp_raw);
  return $data->{files} // {};
}

has _record => (is => 'lazy', init_arg => undef, default => sub ($self) { $self->_load($self->_record_file) });

sub _save_record ($self) {
  $self->_record_file->spew_raw($JSON->encode({ version => 1, files => $self->_record }));
}

=method plan

  my ($upload, $delete) = $sync->plan;

This returns two array references: the paths that need uploading, and the
paths that need deleting, each sorted.

=cut

# Remote paths that get a copy of some other file in the site.
my %COPY_OF = ('bunnycdn_errors/404.html' => '404.html');

sub _source ($self, $rel) { $self->site_dir->child($COPY_OF{$rel} // $rel) }

sub plan ($self) {
  my $want = $self->_load($self->_manifest_file);
  my $have = $self->_record;

  for my $copy (keys %COPY_OF) {
    $want->{$copy} = $want->{ $COPY_OF{$copy} } if $want->{ $COPY_OF{$copy} };
  }

  my @upload = grep {;
    ! $have->{$_} or $JSON->encode($have->{$_}) ne $JSON->encode($want->{$_})
  } sort keys %$want;
  my @delete = grep {; ! $want->{$_} } sort keys %$have;

  return (\@upload, \@delete, $want);
}

=method sync

  my $result = $sync->sync({ dry_run => 0 });

This does the work described above, and returns a hash: C<uploaded> and
C<deleted>, the paths it uploaded and deleted, and C<purged>, the number of
URLs purged, or C<all>.  With C<dry_run>, it returns what it would do,
having done nothing.  It dies if any request fails, having saved what it
did get done.

=cut

sub sync ($self, $arg = {}) {
  my ($upload, $delete, $want) = $self->plan;
  my %result = (uploaded => $upload, deleted => $delete, purged => 0);

  return \%result if $arg->{dry_run};
  return \%result unless @$upload or @$delete;

  my $record = $self->_record;
  $self->state_dir->mkpath;

  my $saved = 0;
  my $ok = sub ($rel, $entry) {
    if ($entry) { $record->{$rel} = $entry }
    else        { delete $record->{$rel} }
    $self->_save_record if ++$saved % 200 == 0;
  };

  my %failed = map {; $_ => 1 } $self->_each('uploading', $upload, sub ($rel) {
    $self->_upload_p($rel)->then(sub { $ok->($rel, $want->{$rel}) });
  });
  my $upload_failed = %failed;

  unless ($upload_failed) {
    $failed{$_} = 1 for $self->_each('deleting', $delete, sub ($rel) {
      $self->_delete_p($rel)->then(sub { $ok->($rel, undef) });
    });
  }

  $self->_save_record;

  # Whatever did get uploaded or deleted is purged even if something failed,
  # so the cache doesn't go on serving what was replaced or removed until
  # the next sync.  -- claude, 2026-10-01
  $result{uploaded} = [ grep {; ! $failed{$_} } @$upload ];
  $result{deleted}  = $upload_failed ? [] : [ grep {; ! $failed{$_} } @$delete ];
  $result{purged}   = $self->_purge([ $result{uploaded}->@*, $result{deleted}->@* ]);

  die sprintf "%d request(s) failed%s; sync again to retry:\n%s",
    0 + keys %failed,
    ($upload_failed ? ', so nothing was deleted' : ''),
    join '', map {; "  $_\n" } sort keys %failed
    if %failed;

  return \%result;
}

# Run $code->($rel) for every path, some at once, retrying failures.  This
# returns the paths that failed every attempt.
sub _each ($self, $label, $paths, $code) {
  return unless @$paths;

  my $progress = Jiggle::Progress->new({
    label  => $label,
    total  => scalar @$paths,
    logger => $self->logger,
  });

  my @failed;
  Mojo::Promise->map({ concurrency => $self->concurrency }, sub {
    my $rel = $_;
    $self->_retrying(sub { $code->($rel) })->then(
      sub { $progress->tick },
      sub ($error) {
        $self->logger->("$label $rel: $error");
        push @failed, $rel;
        $progress->tick;
      },
    );
  }, @$paths)->wait;

  $progress->done;
  return sort @failed;
}

sub _retrying ($self, $code, $attempt = 1) {
  return $code->()->catch(sub ($error) {
    return Mojo::Promise->reject($error) if $attempt >= $self->attempts;
    return Mojo::Promise->timer($self->retry_delay * $attempt)
      ->then(sub { $self->_retrying($code, $attempt + 1) });
  });
}

sub _storage_url ($self, $rel) {
  my $path = join '/', map {; Mojo::Util::url_escape($_) } $self->zone, split m{/}, $rel;
  return $self->storage_url =~ s{/+\z}{}r . "/$path";
}

sub _checked_p ($self, $tx_p, $what) {
  return $tx_p->then(sub ($tx) {
    my $res = $tx->result;
    return $tx if $res->is_success;
    return Mojo::Promise->reject(sprintf '%s: %s %s', $what, $res->code, $res->message);
  });
}

sub _upload_p ($self, $rel) {
  my $file = $self->_source($rel);

  # The checksum makes Bunny refuse an upload that arrives damaged.
  my $tx = $self->ua->build_tx(PUT => $self->_storage_url($rel), {
    AccessKey      => $self->password,
    'Content-Type' => 'application/octet-stream',
    Checksum       => uc Digest::SHA->new(256)->addfile("$file")->hexdigest,
  });
  $tx->req->content->asset(Mojo::Asset::File->new(path => "$file"));

  return $self->_checked_p($self->ua->start_p($tx), 'upload');
}

sub _delete_p ($self, $rel) {
  my $url = $self->_storage_url($rel);
  return $self->ua->delete_p($url, { AccessKey => $self->password })->then(sub ($tx) {
    my $res = $tx->result;

    # Already gone is as good as deleted.
    return $tx if $res->is_success or $res->code == 404;
    return Mojo::Promise->reject(sprintf 'delete: %s %s', $res->code, $res->message);
  });
}

=method urls_for

  my @urls = $sync->urls_for($rel);

This returns the URLs, on every purge host, at which the CDN may have cached
the file at the given path.

=cut

sub urls_for ($self, $rel) {
  my @paths = "/$rel";
  if ($rel =~ m{\A(?:(.*)/)?index\.html\z}) {
    my $dir = $1;
    push @paths, defined $dir ? ("/$dir/", "/$dir") : '/';
  }

  return map {; my $host = $_; map {; "https://$host$_" } @paths } $self->purge_hosts->@*;
}

sub _purge ($self, $changed) {
  return 0 unless @$changed;

  if (@$changed > $self->purge_all_over) {
    my $url = $self->api_url . '/pullzone/' . $self->pull_zone_id . '/purgeCache';
    $self->_checked_p($self->ua->post_p($url, { AccessKey => $self->api_key }), 'purge')->wait;
    $self->logger->('purged the whole pull zone');
    return 'all';
  }

  my @urls = map {; $self->urls_for($_) } @$changed;
  my @failed = $self->_each('purging', \@urls, sub ($url) {
    my $api = Mojo::URL->new($self->api_url . '/purge')->query(url => $url);
    $self->_checked_p($self->ua->post_p($api, { AccessKey => $self->api_key }), 'purge');
  });

  die sprintf "%d purge(s) failed; purge the pull zone by hand, or the CDN may serve stale or removed files:\n%s",
    0 + @failed, join '', map {; "  $_\n" } @failed if @failed;

  return 0 + @urls;
}

1;
