package Jiggle::Editor;
use v5.36;

use Moo;

use Digest::SHA ();
use Jiggle::Album;
use Jiggle::Photo;
use Mojo::JSON ();
use Mojolicious;
use Path::Tiny ();

=head1 NAME

Jiggle::Editor - a local web app for reviewing and editing a batch of photos

=head1 SYNOPSIS

  my $editor = Jiggle::Editor->new({ library => $library, ids => \@ids });
  my $app    = $editor->app;     # a Mojolicious app
  say $editor->token;            # needed once, in the URL: /?token=...

=head1 DESCRIPTION

The editor serves a contact sheet of one batch of photos, fixed when it's
made: a list of ids, not a live query, so photos don't leave the batch when
they change.  Everything it serves is read from the library's files at the
time of the request, so a reload shows what's on disk.

Any web page in the browser can send requests to localhost, so every request
must carry the editor's token.  The first request gives it in the URL, and
gets it back as a cookie marked C<SameSite=Strict>, which the browser sends
only with requests from the editor's own pages.

=cut

has library => (is => 'ro', required => 1);

# The batch, in order.
has ids => (is => 'ro', required => 1);

# A label for the batch, like the query that chose it.
has label => (is => 'ro', default => '');

has token => (
  is => 'lazy',
  default => sub {
    open my $fh, '<:raw', '/dev/urandom' or die "can't read /dev/urandom: $!";
    read($fh, my $bytes, 16) == 16 or die "short read from /dev/urandom\n";
    return unpack 'H*', $bytes;
  },
);

has share_dir => (
  is => 'ro',
  default => sub { Path::Tiny::path(__FILE__)->absolute->parent(3)->child('share') },
);

my $COOKIE = 'jiggle_editor';

# The renditions the editor shows.  Others (like og.jpg) aren't needed, and
# the list keeps a request from naming arbitrary files.
my %SERVABLE = map {; $_ => 1 } qw( h480.webp 500.webp 1024.webp 2048.webp poster.png video.mp4 );

sub app ($self) {
  my $app = Mojolicious->new;
  $app->log->level('warn');
  $app->secrets([ $self->token ]);
  $app->types->type(webp => 'image/webp');

  my %in_batch = map {; $_ => 1 } $self->ids->@*;
  my $editor_dir = $self->share_dir->child('editor');

  my $r = $app->routes;

  $r->get('/' => sub ($c) {
    if (defined(my $token = $c->param('token'))) {
      return $c->render(text => 'bad token', status => 403) unless $token eq $self->token;
      $c->cookie($COOKIE => $token, { httponly => 1, samesite => 'Strict', path => '/' });
      return $c->redirect_to('/');
    }
    return $c->render(text => 'no token', status => 403)
      unless ($c->cookie($COOKIE) // '') eq $self->token;

    $c->res->headers->content_type('text/html; charset=utf-8');
    $c->res->headers->cache_control('no-cache');
    $c->render(data => $editor_dir->child('index.html')->slurp_raw);
  });

  my $authed = $r->under('/' => sub ($c) {
    return 1 if ($c->cookie($COOKIE) // '') eq $self->token;
    $c->render(json => { error => 'no token' }, status => 403);
    return undef;
  });

  $authed->get('/static/*file' => sub ($c) {
    my $file = $c->param('file');
    return $c->reply->not_found if $file =~ m{(?:\A|/)\.};
    my $path = $editor_dir->child('static', $file);
    return $c->reply->not_found unless -f $path;
    $c->res->headers->cache_control('no-cache');
    $c->reply->file("$path");
  });

  $authed->get('/api/batch' => sub ($c) {
    $c->res->headers->cache_control('no-store');
    $c->render(json => $self->batch_data);
  });

  $authed->get('/r/:id/#name' => sub ($c) {
    my ($id, $name) = ($c->param('id'), $c->param('name'));
    return $c->reply->not_found unless $in_batch{$id} and $SERVABLE{$name};
    my $path = $self->library->derived_path($id, $name);
    return $c->reply->not_found unless -f $path;
    $c->res->headers->cache_control('no-cache');
    $c->reply->file("$path");
  });

  return $app;
}

=method batch_data

This returns the batch as the editor's page gets it: the photos, in order, as
they are on disk now, and the library's albums.  Each photo has a C<version>,
the digest of its metadata file, which a write must present so that changes
made to the file since it was loaded aren't overwritten.

=cut

sub batch_data ($self) {
  my @albums = $self->_albums;

  my %albums_of;
  for my $album (@albums) {
    push $albums_of{$_}->@*, $album->slug for $album->photos->@*;
  }

  my @photos;
  for my $id ($self->ids->@*) {
    my $file = $self->library->meta_path($id);
    next unless -e $file;
    my $bytes = $file->slurp_raw;
    my $photo = Jiggle::Photo->from_toml_file($file);
    push @photos, {
      $self->photo_fields($photo)->%*,
      albums  => [ sort { $a cmp $b } ($albums_of{$id} // [])->@* ],
      version => Digest::SHA::sha1_hex($bytes),
    };
  }

  return {
    label  => $self->label,
    photos => \@photos,
    albums => [
      sort {; $a->{title} cmp $b->{title} }
      map  {; { slug => $_->slug, title => $_->title } } @albums
    ],
  };
}

=method photo_fields

This returns a photo's editable fields, and the facts shown beside them, as a
hash reference ready for JSON.

=cut

sub photo_fields ($self, $photo) {
  my $bool = sub ($v) { $v ? Mojo::JSON::true : Mojo::JSON::false };
  my $loc  = $photo->location;

  return {
    id          => $photo->id,
    type        => $photo->type,
    title       => $photo->title,
    description => $photo->description,
    tags        => [ $photo->tags->@* ],
    visibility  => $photo->visibility,
    pending     => $bool->($photo->pending),
    taken       => $photo->taken,
    rotate      => 0 + $photo->rotate,
    location    => $loc ? { private => $bool->($loc->{private}) } : undef,

    width       => 0 + $photo->width,
    height      => 0 + $photo->height,
    file        => $photo->original->{file},
    duration    => $photo->duration,
    added       => $photo->added_at,
    flickr_id   => $photo->flickr_id,
  };
}

sub _albums ($self) {
  my $dir = $self->library->albums_dir;
  return () unless -d $dir;
  return map {; Jiggle::Album->from_toml_file($_) } sort $dir->children(qr/\.toml\z/);
}

1;
