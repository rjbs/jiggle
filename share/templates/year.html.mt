<%= $site->partial('_period_nav', {
  earlier => $earlier && { url => $site->year_url($earlier->{year}), label => $earlier->{year} },
  later   => $later   && { url => $site->year_url($later->{year}), label => $later->{year} },
}) %>
<h1><%= $year->{year} %> <span class="count"><%= $year->{count} %> photo<%= $year->{count} == 1 ? '' : 's' %></span></h1>
% for my $month (reverse $year->{months}->@*) {
%   my $n = $month->{photos}->@*;
<section class="archive-month">
  <h2><a href="<%= $site->month_url($month->{year}, $month->{month}) %>"><%= $site->month_name($month->{month}) %></a>
    <span class="count"><%= $n %> photo<%= $n == 1 ? '' : 's' %></span></h2>
  <%= $site->partial('_grid', { photos => [ $site->sample(12, $month->{photos}->@*) ] }) %>
%   if ($n > 12) {
  <p class="more"><a href="<%= $site->month_url($month->{year}, $month->{month}) %>">All <%= $n %> from <%= $site->month_name($month->{month}) %> &rarr;</a></p>
%   }
</section>
% }
